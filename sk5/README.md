# sk5: how the fork follows TECHO5 and updates itself

- **release.env**: where the fork's daemons look for updates (this fork's releases), the public key
  they believe them by, and the key their root filesystem is published under (`arm-checkers-gpu`). A
  unit running the fork never takes one of TECHO5's releases, nor a TECHO5 unit the fork's.
- **techo5.env**: TECHO5's repository and its release key; a TECHO5 release is built on only once
  that key is found to have signed its manifest (**base.sh**, `echod/cmd/sk5base`).
- **parts**: the fork's parts, each the TECHO5 files it changes and the files of its own.
- **follow.sh**: the fork on a newer TECHO5 release: a merge where it goes through, else part by part,
  a part that no longer applies or builds left out with a warning (its build tag), never the core.
- **manifest.sh**, **check-image.sh**: the signed manifest a release carries, and the image checked.

`.github/workflows/sk5-follow.yml` runs every other Monday, or by hand (Actions, *Follow TECHO5*, *Run
workflow*; *force* builds even with nothing new). A clean result is a release, which units running the
fork find within a day: Home Assistant offers it, and with *Install updates automatically* on they
install it overnight. A result with a part left out is a prerelease, which no unit takes by itself.

The release key's private half is the `SK5_SIGNING_KEY` secret. Lose it and the units can be updated
again only by hand, with an image carrying a new key.

Home Assistant ranks `vX.Y.Z_sk5.N` as `vX.Y.Z`: a new TECHO5 release is offered, a rebuild on the
same one is not (install that by hand: `slotctl install <file> && reboot`).
