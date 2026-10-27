use base64::{engine::general_purpose::STANDARD, Engine};
use groth16_solana::{
    groth16::{negate_g1_be, Groth16Verifier},
    vk::gnark::parse_gnark_vk_bytes,
};
use serde::Deserialize;

#[derive(Deserialize)]
struct Vector {
    id: String,
    vk: String,
    a: String,
    b: String,
    c: String,
    public: Vec<[u8; 32]>,
    commitment: Option<String>,
    pok: Option<String>,
}

fn decode<const N: usize>(text: &str) -> [u8; N] {
    STANDARD
        .decode(text)
        .expect("base64")
        .try_into()
        .expect("point length")
}

fn verify(vector: &Vector, public: &[[u8; 32]; 1]) -> bool {
    let raw = STANDARD.decode(&vector.vk).expect("vk base64");
    let owned = parse_gnark_vk_bytes(&raw).expect("parse gnark verifying key");
    let vk = owned.as_borrowed();
    let a = negate_g1_be(&decode::<64>(&vector.a));
    let b = decode::<128>(&vector.b);
    let c = decode::<64>(&vector.c);
    if let (Some(commitment), Some(pok)) = (&vector.commitment, &vector.pok) {
        let commitment = decode::<64>(commitment);
        let pok = decode::<64>(pok);
        Groth16Verifier::new_with_commitment(&a, &b, &c, &commitment, &pok, public, &vk)
            .expect("construct BSB22 verifier")
            .verify()
            .is_ok()
    } else {
        Groth16Verifier::new(&a, &b, &c, public, &vk)
            .expect("construct verifier")
            .verify()
            .is_ok()
    }
}

fn main() {
    let directory = std::env::args()
        .nth(1)
        .expect("usage: spr-solana-rehearsal <vectors-dir>");
    for id in ["transfer_confidential_1_2", "merge_8_1"] {
        let path = std::path::Path::new(&directory).join(format!("{id}.json"));
        let vector: Vector = serde_json::from_slice(&std::fs::read(path).expect("read vector"))
            .expect("parse vector");
        assert_eq!(vector.id, id);
        assert_eq!(vector.public.len(), 1);
        let mut public = [vector.public[0]];
        assert!(
            verify(&vector, &public),
            "{} ceremony proof failed",
            vector.id
        );
        public[0][31] ^= 1;
        assert!(!verify(&vector, &public), "tampered public input verified");
        println!("verified {id}: ceremony proof accepted; tampered public input rejected");
    }
}
