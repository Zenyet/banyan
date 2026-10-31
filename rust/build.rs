// Give the Windows executable its icon.
fn main() {
    println!("cargo:rerun-if-changed=../packaging/windows/banyan.ico");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows") {
        let mut resource = winresource::WindowsResource::new();
        resource.set_icon("../packaging/windows/banyan.ico");
        resource.compile().expect("failed to embed the Windows icon");
    }
}
