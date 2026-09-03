# PocketBeagle 2 USB fastboot payload

`.alo/run` performs the three-stage AM62x USB-DFU boot and leaves U-Boot's
fastboot gadget running. It requires the board to have been reset into ROM USB
boot mode (`0451:6165`, DFU alternate `bootloader`). No host packages or network
access are needed at replay time; the required `dfu-util`, `libusb`, and images
are included.

The images were reproducibly built from commit
`9d6d01a8cfd31b5b5a300c0660f2f0538e94f391` of
<https://github.com/beagleboard/u-boot.git> (branch
`v2025.10-am62-pocketbeagle2`):

* R5: `am62_pocketbeagle2_r5_defconfig am62x_r5_usbdfu.config`, using
  `BINMAN_INDIRS` from the `ti-linux-firmware` branch of
  <https://github.com/beagleboard/ti-linux-firmware.git>. This produces the
  HS-FS `tiboot3.bin` needed by the connected board.
* A53 SPL and U-Boot: `am62_pocketbeagle2_a53_defconfig
  am62x_a53_usbdfu.config am62x_a53_android.config`. The fragments enable the
  second DFU stage, `CONFIG_CMD_FASTBOOT`, and
  the USB fastboot function. The configuration was additionally set to
  `CONFIG_BOOTDELAY=0` and `CONFIG_BOOTCOMMAND="fastboot usb 0"`, so no
  timing-sensitive serial-console interaction is needed. The resulting
  `tispl.bin` is emitted by binman as a complete FIT rather than modified after
  signing.
* The A53 build used the BL31, OP-TEE, and DM payloads from the official
  `pocketbeagle2-debian-12.10-minimal-arm64-2025-03-18-8gb.img.xz` release.
  The final U-Boot build can use payloads extracted from its `tispl.bin` FIT
  with `dumpimage -p 0`, `-p 1`, and `-p 2`; remove each existing 1700-byte
  X.509 wrapper before passing those payloads back to binman.

Toolchains were Bootlin stable 2024.05-1 (`armv7-eabihf--glibc` and
`aarch64--glibc`). The resulting image SHA-256 hashes are recorded in
`artifacts/SHA256SUMS`.
