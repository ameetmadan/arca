## Install (Apple Silicon Mac, macOS 14 or later)

1. Download **Arca-…-macOS-arm64.zip** below and double-click it to unzip.
2. Drag **Arca.app** into your Applications folder.
3. This build is not notarized by Apple, so the first launch is blocked. Either:
   - open **Terminal** and run `xattr -dr com.apple.quarantine /Applications/Arca.app`, then open Arca normally; or
   - open Arca once, dismiss the warning, then go to **System Settings → Privacy & Security** and click **Open Anyway**.
4. Follow the setup: pick a scratch folder with a few test files (see [TESTING.md](https://github.com/ameetmadan/arca/blob/{{TAG}}/docs/TESTING.md#before-you-start)), pick a model (about 2 GB download), done. Arca moves files when you accept a suggestion, so keep folders whose layout matters to you out of this beta.

macOS may ask whether Arca may access your Documents, Desktop or Downloads folder. Allow it, otherwise Arca cannot read the folders you chose.

## What to try

See [TESTING.md](https://github.com/ameetmadan/arca/blob/{{TAG}}/docs/TESTING.md) for a 15-minute walkthrough and what feedback is most useful.

Send feedback with **Help → Send Feedback…** in Arca. **Arca → Check for Updates…** opens the Releases page, where the newest beta is at the top. **Arca → About Arca** shows which one you have.

## Where things live

- Settings, databases and models: `~/Library/Application Support/Arca/`
- Server log: `~/Library/Logs/Arca/arca-server.log` (Help → Show Server Log)
- To start completely fresh: quit Arca, delete both folders, and run `defaults delete com.ameetmadan.Arca`.

Everything runs on your Mac. The only network access is the one-time model download from Hugging Face.
