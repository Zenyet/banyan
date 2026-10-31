//! Credential prompts for git and ssh, like JetBrains' "Git Authentication"
//! dialog.
//!
//! Git runs this same executable as `GIT_ASKPASS` / `SSH_ASKPASS`. In that
//! mode it forwards the prompt to the running app, over a Unix socket or on
//! Windows a loopback port, and the app asks the user and sends the answer back.

use std::cell::RefCell;
use std::io::{BufRead as _, BufReader, Write as _};
use std::rc::Rc;
use std::sync::OnceLock;
use std::sync::mpsc::{self, Receiver, Sender};
use std::time::Duration;

use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

/// Set in git's environment; its presence puts this executable in client mode.
pub const SOCKET_ENV: &str = "BANYAN_ASKPASS_SOCKET";

/// How long git may wait for the user before the prompt counts as cancelled.
const ANSWER_TIMEOUT: Duration = Duration::from_secs(600);

pub struct Request {
    pub prompt: String,
    reply: Sender<Option<String>>,
}

/// Where the server listens, as the client is told in [`SOCKET_ENV`].
static ADDRESS: OnceLock<String> = OnceLock::new();

/// Environment for git commands, once the server runs.
pub fn env() -> Option<[(&'static str, std::ffi::OsString); 5]> {
    let socket = ADDRESS.get()?;
    let exe = std::env::current_exe().ok()?;
    Some([
        ("GIT_ASKPASS", exe.clone().into()),
        ("SSH_ASKPASS", exe.into()),
        ("SSH_ASKPASS_REQUIRE", "force".into()),
        (SOCKET_ENV, socket.into()),
        ("DISPLAY", std::env::var_os("DISPLAY").unwrap_or_else(|| ":0".into())),
    ])
}

/// Client mode: forward the prompt and print the answer for git.
pub fn run_client(socket: &str) -> i32 {
    let prompt = std::env::args().nth(1).unwrap_or_default();
    match ask(socket, &prompt) {
        Some(answer) => {
            println!("{answer}");
            0
        }
        None => 1,
    }
}

fn ask(address: &str, prompt: &str) -> Option<String> {
    let mut stream = transport::connect(address)?;
    writeln!(stream, "{}", prompt.replace('\n', " ")).ok()?;
    let mut line = String::new();
    BufReader::new(stream).read_line(&mut line).ok()?;
    line.trim_end_matches('\n').strip_prefix("OK ").map(str::to_string)
}

/// Listen for prompts; each arrives on the returned channel.
pub fn start_server() -> std::io::Result<Receiver<Request>> {
    let (tx, rx) = mpsc::channel();
    let address = transport::serve(move |stream| {
        let requests = tx.clone();
        std::thread::spawn(move || handle(stream, requests));
    })?;
    ADDRESS.set(address).ok();
    Ok(rx)
}

fn handle(stream: transport::Stream, requests: Sender<Request>) {
    let mut reader = BufReader::new(&stream);
    if !transport::accepts(&mut reader) {
        return;
    }
    let mut prompt = String::new();
    if reader.read_line(&mut prompt).is_err() {
        return;
    }
    let (reply, answer) = mpsc::channel();
    let prompt = prompt.trim_end_matches(['\n', '\r']).to_string();
    if requests.send(Request { prompt, reply }).is_err() {
        return;
    }
    let mut stream = stream;
    match answer.recv_timeout(ANSWER_TIMEOUT) {
        Ok(Some(value)) => writeln!(stream, "OK {value}").ok(),
        _ => writeln!(stream, "CANCEL").ok(),
    };
}

/// A socket file only this user can open.
#[cfg(unix)]
mod transport {
    use std::io::BufRead;
    use std::os::unix::fs::PermissionsExt as _;
    use std::os::unix::net::{UnixListener, UnixStream};

    pub type Stream = UnixStream;

    pub fn serve(on_connect: impl Fn(Stream) + Send + 'static) -> std::io::Result<String> {
        let path = std::env::temp_dir().join(format!("banyan-askpass-{}.sock", std::process::id()));
        std::fs::remove_file(&path).ok();
        let listener = UnixListener::bind(&path)?;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
        std::thread::spawn(move || listener.incoming().flatten().for_each(on_connect));
        Ok(path.to_string_lossy().into_owned())
    }

    pub fn connect(address: &str) -> Option<Stream> {
        UnixStream::connect(address).ok()
    }

    /// The file's permissions already keep other users out.
    pub fn accepts(_: &mut impl BufRead) -> bool {
        true
    }
}

/// A loopback port; clients prove they were started by this app with a
/// random token, passed as `port:token`.
#[cfg(windows)]
mod transport {
    use std::io::{BufRead, Write as _};
    use std::net::{Ipv4Addr, TcpListener, TcpStream};
    use std::sync::OnceLock;

    pub type Stream = TcpStream;

    static TOKEN: OnceLock<String> = OnceLock::new();

    fn token() -> &'static str {
        use std::hash::{BuildHasher as _, Hasher as _};
        TOKEN.get_or_init(|| {
            // RandomState is seeded from the OS's random source.
            (0..4)
                .map(|ix| {
                    let mut hasher = std::collections::hash_map::RandomState::new().build_hasher();
                    hasher.write_usize(ix);
                    format!("{:016x}", hasher.finish())
                })
                .collect()
        })
    }

    pub fn serve(on_connect: impl Fn(Stream) + Send + 'static) -> std::io::Result<String> {
        let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, 0))?;
        let port = listener.local_addr()?.port();
        std::thread::spawn(move || listener.incoming().flatten().for_each(on_connect));
        Ok(format!("{port}:{}", token()))
    }

    pub fn connect(address: &str) -> Option<Stream> {
        let (port, token) = address.split_once(':')?;
        let mut stream = TcpStream::connect((Ipv4Addr::LOCALHOST, port.parse().ok()?)).ok()?;
        writeln!(stream, "{token}").ok()?;
        Some(stream)
    }

    pub fn accepts(reader: &mut impl BufRead) -> bool {
        let mut line = String::new();
        reader.read_line(&mut line).is_ok() && line.trim_end() == token()
    }
}

/// Show prompts from git in the active window as they arrive.
pub fn spawn_prompt_loop(requests: Receiver<Request>, cx: &mut App) {
    cx.spawn(async move |cx| {
        loop {
            cx.background_executor().timer(Duration::from_millis(150)).await;
            while let Ok(request) = requests.try_recv() {
                cx.update(|cx| {
                    let window = cx.active_window().or_else(|| cx.windows().into_iter().next());
                    if let Some(window) = window {
                        // Without a window the request is dropped, which cancels it.
                        window
                            .update(cx, |_, window, cx| show_prompt(request, window, cx))
                            .ok();
                    }
                });
            }
        }
    })
    .detach();
}

fn is_secret(prompt: &str) -> bool {
    let prompt = prompt.to_lowercase();
    prompt.contains("password") || prompt.contains("passphrase") || prompt.contains("token")
}

fn show_prompt(request: Request, window: &mut Window, cx: &mut App) {
    let secret = is_secret(&request.prompt);
    let input = cx.new(|cx| InputState::new(window, cx).masked(secret));
    // Answered once, by OK, Cancel or closing the dialog.
    let reply = Rc::new(RefCell::new(Some(request.reply)));
    let answer = {
        let reply = reply.clone();
        move |value: Option<String>| {
            if let Some(reply) = reply.borrow_mut().take() {
                reply.send(value).ok();
            }
        }
    };
    let submit = {
        let input = input.clone();
        let answer = answer.clone();
        move |window: &mut Window, cx: &mut App| {
            answer(Some(input.read(cx).value().to_string()));
            window.close_dialog(cx);
        }
    };

    let prompt: SharedString = request.prompt.trim().trim_end_matches(':').to_string().into();
    let needs_focus = std::cell::Cell::new(true);
    window.open_dialog(cx, move |dialog, window, cx| {
        if needs_focus.replace(false) {
            input.focus_handle(cx).focus(window, cx);
        }
        let ok = submit.clone();
        let cancel = answer.clone();
        let close = answer.clone();
        // Enter confirms the dialog, which then closes it; answer first.
        let enter = answer.clone();
        let entered = input.clone();
        dialog
            .title("Git Authentication")
            .w(px(460.))
            .on_ok(move |_, _, cx| {
                enter(Some(entered.read(cx).value().to_string()));
                true
            })
            .on_close(move |_, _, _| close(None))
            .child(
                v_flex()
                    .gap_3()
                    .child(div().text_sm().child(prompt.clone()))
                    .child(Input::new(&input))
                    .when(secret, |col| {
                        col.child(
                            div()
                                .text_xs()
                                .text_color(cx.theme().muted_foreground)
                                .child("For GitHub and GitLab over HTTPS, use a personal access token."),
                        )
                    })
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(Button::new("askpass-cancel").label("Cancel").on_click(
                                move |_, window, cx| {
                                    cancel(None);
                                    window.close_dialog(cx);
                                },
                            ))
                            .child(
                                Button::new("askpass-ok")
                                    .label("OK")
                                    .primary()
                                    .on_click(move |_, window, cx| ok(window, cx)),
                            ),
                    ),
            )
    });
}

#[cfg(test)]
mod tests {
    use super::{ask, handle, is_secret, transport};
    use std::sync::mpsc;

    #[test]
    fn prompt_round_trip() {
        let (tx, rx) = mpsc::channel();
        let address = transport::serve(move |stream| {
            let requests = tx.clone();
            std::thread::spawn(move || handle(stream, requests));
        })
        .unwrap();
        std::thread::spawn(move || {
            let first = rx.recv().unwrap();
            assert_eq!(first.prompt, "Username for 'https://example.com': ");
            first.reply.send(Some("alice".into())).unwrap();
            let second = rx.recv().unwrap();
            drop(second); // Dropping the request cancels it.
        });
        assert_eq!(ask(&address, "Username for 'https://example.com': ").as_deref(), Some("alice"));
        assert_eq!(ask(&address, "Password for 'https://alice@example.com': "), None);
    }

    #[test]
    fn secrets() {
        assert!(is_secret("Password for 'https://example.com': "));
        assert!(is_secret("Enter passphrase for key '/Users/me/.ssh/id_ed25519': "));
        assert!(!is_secret("Username for 'https://example.com': "));
    }
}
