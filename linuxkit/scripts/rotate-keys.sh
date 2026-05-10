#!/usr/bin/env bash
# =============================================================================
# scripts/rotate-keys.sh
#
# Generates a new kernel module signing key pair, re-signs all modules in the
# current kernel build, and outputs the new key for baking into the next image.
#
# This script should be run as part of a periodic key rotation (e.g. annually
# or when a key compromise is suspected).
#
# Usage:
#   bash scripts/rotate-keys.sh [--days N] [--out-dir DIR]
#
# Outputs:
#   signing_key.pem    new private key (protect with your secrets manager)
#   signing_cert.pem   new certificate (embed in kernel build)
#   modules-signed.log list of re-signed modules
#
# Requirements: openssl, find, sign-file (from kernel-devel)
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'
CYN='\033[0;36m'; BLD='\033[1m'; RST='\033[0m'

step() { echo -e "\n${BLD}${CYN}>> $*${RST}"; }
ok()   { echo -e "  ${GRN}OK${RST}  $*"; }
warn() { echo -e "  ${YLW}WARN${RST}  $*"; }
die()  { echo -e "  ${RED}FAIL${RST}  $*" >&2; exit 1; }

DAYS=365
OUT_DIR="$(pwd)/keys-$(date +%Y%m%d)"
KERNEL_VERSION="${KERNEL_VERSION:-$(uname -r)}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --days)    DAYS="$2"; shift 2 ;;
    --out-dir) OUT_DIR="$2"; shift 2 ;;
    *)         echo "Unknown arg: $1"; exit 1 ;;
  esac
done

mkdir -p "$OUT_DIR"

step "Generating RSA-4096 module signing key pair (valid ${DAYS} days)"

openssl req -new -nodes \
  -utf8 -sha512 -days "$DAYS" \
  -batch \
  -x509 \
  -newkey rsa:4096 \
  -keyout "${OUT_DIR}/signing_key.pem" \
  -out    "${OUT_DIR}/signing_cert.pem" \
  -subj "/CN=bonnie-cicd Kernel Module Signing Key $(date +%Y)/" \
  -addext "keyUsage=digitalSignature" \
  -addext "extendedKeyUsage=codeSigning"

ok "Key pair generated:"
echo    "  Private key : ${OUT_DIR}/signing_key.pem"
echo    "  Certificate : ${OUT_DIR}/signing_cert.pem"

# Verify the key
KEY_INFO=$(openssl x509 -in "${OUT_DIR}/signing_cert.pem" -noout -text \
  | grep -E "Validity|Not (Before|After)" | head -5)
echo -e "\n  ${KEY_INFO}"

step "Restricting key file permissions"
chmod 600 "${OUT_DIR}/signing_key.pem"
chmod 644 "${OUT_DIR}/signing_cert.pem"
ok "Permissions set (key: 600, cert: 644)"

step "Re-signing kernel modules for ${KERNEL_VERSION}"

SIGN_FILE=""
for path in \
  "/usr/src/linux-headers-${KERNEL_VERSION}/scripts/sign-file" \
  "/lib/modules/${KERNEL_VERSION}/build/scripts/sign-file" \
  "$(which sign-file 2>/dev/null)"; do
  [[ -x "$path" ]] && SIGN_FILE="$path" && break
done

if [[ -z "$SIGN_FILE" ]]; then
  warn "sign-file not found — skipping module re-signing"
  warn "Install linux-headers-$(uname -r) or build the kernel to get sign-file"
else
  LOG="${OUT_DIR}/modules-signed.log"
  COUNT=0
  find "/lib/modules/${KERNEL_VERSION}" -name "*.ko" -o -name "*.ko.zst" | \
  while read -r mod; do
    "$SIGN_FILE" sha512 \
      "${OUT_DIR}/signing_key.pem" \
      "${OUT_DIR}/signing_cert.pem" \
      "$mod" 2>/dev/null \
      && echo "$mod" >> "$LOG" \
      && (( COUNT++ )) || warn "Failed to sign: $mod"
  done
  ok "Signed $COUNT modules — log at $LOG"
fi

step "Instructions for embedding new key in next image"
cat << INSTR

  1. Store the private key securely (Vault, AWS Secrets Manager, etc.):
     vault kv put secret/bonnie-cicd/module-signing-key \
       key=@${OUT_DIR}/signing_key.pem \
       cert=@${OUT_DIR}/signing_cert.pem

  2. Add the certificate to the kernel build:
     cp ${OUT_DIR}/signing_cert.pem \\
       linux-${KERNEL_VERSION}/certs/signing_key.pem

  3. Set in kernel .config (already in bonnie-cicd.config):
     CONFIG_MODULE_SIG_KEY="certs/signing_key.pem"
     CONFIG_MODULE_SIG_SHA512=y
     CONFIG_MODULE_SIG_FORCE=y

  4. Rebuild the kernel package:
     PUSH=1 bash scripts/build-kernel.sh

  5. Update bonnie.yml kernel.image with the new digest:
     make pin

  6. DESTROY the local private key copy after storing it securely.
     The cert (signing_cert.pem) can be kept for verification.
INSTR

step "Done — key rotation complete"
echo -e "  ${YLW}IMPORTANT: Protect ${OUT_DIR}/signing_key.pem${RST}"
echo    "  Delete it after uploading to your secrets manager."
