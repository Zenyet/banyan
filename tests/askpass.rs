//! Git asks for credentials through the banyan executable in askpass mode.

use std::io::{BufRead as _, BufReader, Write as _};
use std::os::unix::net::UnixListener;
use std::process::{Command, Stdio};

#[test]
fn git_gets_credentials_from_the_app() {
    let socket = std::env::temp_dir().join(format!("banyan-askpass-it-{}.sock", std::process::id()));
    std::fs::remove_file(&socket).ok();
    let listener = UnixListener::bind(&socket).unwrap();
    // Stand in for the app: answer the username and password prompts.
    let server = std::thread::spawn(move || {
        let mut prompts = Vec::new();
        for stream in listener.incoming().take(2) {
            let mut stream = stream.unwrap();
            let mut prompt = String::new();
            BufReader::new(&stream).read_line(&mut prompt).unwrap();
            let answer = if prompt.starts_with("Username") { "alice" } else { "s3cret" };
            writeln!(stream, "OK {answer}").unwrap();
            prompts.push(prompt.trim_end().to_string());
        }
        prompts
    });

    let mut git = Command::new("git")
        .args(["-c", "credential.helper=", "credential", "fill"])
        .env("GIT_ASKPASS", env!("CARGO_BIN_EXE_banyan"))
        .env("BANYAN_ASKPASS_SOCKET", &socket)
        .env("GIT_TERMINAL_PROMPT", "0")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    git.stdin
        .take()
        .unwrap()
        .write_all(b"protocol=https\nhost=example.com\n\n")
        .unwrap();
    let output = git.wait_with_output().unwrap();
    let stdout = String::from_utf8_lossy(&output.stdout);

    assert!(output.status.success(), "git failed: {stdout}");
    assert!(stdout.contains("username=alice"), "{stdout}");
    assert!(stdout.contains("password=s3cret"), "{stdout}");
    let prompts = server.join().unwrap();
    assert!(prompts[0].starts_with("Username for 'https://example.com'"), "{prompts:?}");
    assert!(prompts[1].starts_with("Password for 'https://alice@example.com'"), "{prompts:?}");
    std::fs::remove_file(&socket).ok();
}
