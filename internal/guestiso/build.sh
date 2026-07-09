#!/usr/bin/env bash
# Runs INSIDE a privileged debian container. Builds a combined Windows install
# ISO to /out/$OUT_NAME. Driven entirely by env (no hardcoded values):
#   UUP_ID         uupdump build id for the Windows base
#   EDITION        Windows edition (e.g. professional)
#   DRIVER_SOURCE  URL of a driver ISO (virtio-win) staged under \virtio
#   OUT_NAME       output ISO file name
# Mounted in: /ctx/autounattend.xml   Output: /out/$OUT_NAME
set -euo pipefail
: "${UUP_ID:?}" "${EDITION:?}" "${DRIVER_SOURCE:?}" "${OUT_NAME:?}"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq aria2 cabextract wimtools chntpw genisoimage xorriso unzip curl ca-certificates >/dev/null

mkdir -p /work && cd /work
mkdir -p uup && cd uup
curl -sL -o pkg.zip "https://uupdump.net/get.php?id=${UUP_ID}&pack=en-us&edition=${EDITION}&autodl=2"
unzip -o pkg.zip >/dev/null && chmod +x uup_download_linux.sh
ok=0
for attempt in $(seq 1 8); do
  if ./uup_download_linux.sh >/work/uup.log 2>&1; then ok=1; break; fi
  echo "  UUP attempt $attempt failed (transient git.uupdump.net 522s); retrying..."; sleep 15
done
[ "$ok" = 1 ] || { echo "UUP build failed after retries"; tail -20 /work/uup.log; exit 1; }
BASE=$(ls /work/uup/*.ISO | head -1)

cd /work
curl -fL --retry 8 -o driver.iso "$DRIVER_SOURCE"

mkdir -p iso mnt
mount -o loop,ro "$BASE" mnt; cp -aT mnt iso; umount mnt; chmod -R u+w iso
mount -o loop,ro driver.iso mnt
mkdir -p iso/virtio/viostor/w11/amd64 iso/virtio/NetKVM/w11/amd64
cp -a mnt/viostor/w11/amd64/. iso/virtio/viostor/w11/amd64/ 2>/dev/null || true
cp -a mnt/NetKVM/w11/amd64/. iso/virtio/NetKVM/w11/amd64/ 2>/dev/null || true
umount mnt
cp /ctx/autounattend.xml iso/autounattend.xml

EFISYS=efi/microsoft/boot/efisys.bin
[ -f iso/efi/microsoft/boot/efisys_noprompt.bin ] && EFISYS=efi/microsoft/boot/efisys_noprompt.bin
xorriso -as mkisofs -iso-level 3 -full-iso9660-filenames -volid "WIN_COMBINED" \
  -b boot/etfsboot.com -no-emul-boot -boot-load-size 8 -boot-info-table \
  -eltorito-alt-boot -e "$EFISYS" -no-emul-boot \
  -o "/out/${OUT_NAME}" iso
echo "DONE: /out/${OUT_NAME}"
ls -la "/out/${OUT_NAME}"
