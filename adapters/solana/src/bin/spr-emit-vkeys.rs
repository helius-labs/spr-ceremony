use groth16_solana::vk::{
    gnark::generate_bsb22_vk_file,
    setup::{ProvingKeySource, SetupKind},
};
use std::path::Path;

fn main() {
    let args: Vec<_> = std::env::args().skip(1).collect();
    assert!(
        args.len() == 2 || args.len() == 3,
        "usage: spr-emit-vkeys <package-dir> <new-output-dir> [--insecure-test-setup]"
    );
    let setup = if args.len() == 3 {
        assert_eq!(args[2], "--insecure-test-setup");
        SetupKind::InsecureTest
    } else {
        SetupKind::Production
    };
    let package = Path::new(&args[0]);
    let out = Path::new(&args[1]);
    assert!(!out.exists(), "output directory must be new");
    let metadata: serde_json::Value = serde_json::from_slice(
        &std::fs::read(package.join("package.json")).expect("read package metadata"),
    )
    .expect("parse package metadata");
    for name in metadata["keys"].as_object().expect("key mapping").keys() {
        let id = name.strip_suffix(".key").expect("key suffix");
        assert!(
            id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_'),
            "invalid circuit ID"
        );
        generate_bsb22_vk_file(
            package.join(format!("{id}.vkbin")),
            out,
            &format!("{id}.rs"),
            "VERIFYINGKEY",
            setup,
            ProvingKeySource::File(&package.join(name)),
        )
        .expect("emit verifying key");
    }
}
