# Linux x64 AppImage release checks

The fork's Linux x64 AppImage baseline is Ubuntu 24.04 LTS with glibc 2.39.
The desktop workflow builds on that image with a fresh pnpm store and writes
an ABI record only after inspecting the AppImage and running its desktop and
native module smoke tests. The record binds the checked bytes to the source
revision and the updater manifest. Keep the AppImage, ABI record,
latest-linux.yml and build-revision from the same Actions artifact.

## Clean Ubuntu 24.04 VM

Use a fresh x86-64 desktop VM as a normal user. Install the documented
AppImage runtime dependency and the smoke-test tools from Ubuntu's repositories.

    sudo apt-get update
    sudo apt-get install -y libfuse2t64 xvfb xauth xdotool python3-yaml
    getconf GNU_LIBC_VERSION
    uname -m

Check out the same revision of this repository that contains the verification
scripts. Save the extracted Actions artifact in a separate directory. Set
ARTIFACT to that directory and REVISION to the build-revision value confirmed
from the Actions run. For a pull request run, GitHub can check out the test
merge commit. Compare its tree with the PR candidate's tree.

    APPIMAGE=$(find "$ARTIFACT" -maxdepth 1 -type f -name '*.AppImage')
    test -f "$APPIMAGE"
    python3 scripts/fork-release/linux_appimage_abi.py verify "$ARTIFACT" "$REVISION"
    sha256sum "$APPIMAGE"
    chmod u+x "$APPIMAGE"
    xvfb-run -a bash scripts/fork-release/smoke-linux-appimage.sh "$APPIMAGE"
    python3 scripts/fork-release/linux_appimage_abi.py verify "$ARTIFACT" "$REVISION"

The smoke test waits for a visible Multica window, opens a shell via the
packaged node-pty module and loads the packaged sherpa-onnx voice addon.
Save the output with the VM OS and glibc version, revision and AppImage hash.
With FUSE available the script launches the AppImage normally. The hosted
runner can exercise the AppImage's extract-and-run mode when FUSE is
unavailable. Record which path ran in a VM test. The voice check covers
loading the native engine. A product-level speech check also needs a voice
model and a microphone or audio fixture.

## Recover an old install that cannot start

The fork's official v0.9.2 x64 AppImage SHA-256
0679ca3d44f296edc775b3ed70dff91cc845abc90ec1f11fc7f0bcb71787cb88
was independently found to need GLIBC_2.42 in pty.node. On Ubuntu 24.04
its updater cannot start. Record the failing launch and the hash in a VM
before replacing the AppImage.

1. Close Multica and its daemon. Back up the existing AppImage, desktop
   launcher, actual Electron profile (normally under the XDG config folder)
   and Multica daemon data. Record the workspace locations and leave them
   in place. Use test user data with a known workspace and settings for a
   recovery rehearsal.
2. Verify the replacement AppImage hash against the trusted build record and
   updater manifest. For a user-owned install, stage a new executable in the
   same directory, then atomically move it to the existing AppImage path.
   This preserves an existing launcher that points to that path.

       install -m 0755 "$APPIMAGE" "$INSTALLED_APPIMAGE.new"
       sha256sum "$INSTALLED_APPIMAGE.new"
       mv -T -- "$INSTALLED_APPIMAGE.new" "$INSTALLED_APPIMAGE"

3. Start from the original launcher. Record the installed version and hash,
   verify the test settings and workspaces, open a terminal and run an
   identifiable command. Verify voice loading and restart. When the isolated
   feed reports that version, record the same-version check and confirm zero
   installer downloads.
4. If recovery fails, stop both processes, restore the profile and daemon
   data from the backup, and use a previously verified 24.04-compatible
   AppImage. Preserve the backup while a compatible build is obtained if
   none is available. Keep the v0.9.2 AppImage as evidence since it still
   cannot run on this baseline.

Use a bootable test source in the same VM for a separate automatic-update
rehearsal. Record that source version and keep its test feed isolated. The
old v0.9.2 automatic-update path can be rehearsed in a separate environment
in which that build starts.
