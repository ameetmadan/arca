# Testing Arca

A walkthrough that takes about 15 minutes after the model download, covering
every feature once. Installation steps are in the
[release notes](release-notes.md).

## Before you start

Make a scratch folder so nothing important gets moved, e.g. `~/ArcaTest`, and
put in it:

- 3–5 documents you know the content of (a PDF, a Word file, a Markdown or
  text note): a lease, an invoice, a travel booking work well.
- One sub-folder with a matching file, e.g. `Travel/` containing a train or
  flight booking, and `Bills/` containing an invoice.
- A CSV or Excel file with a few columns, e.g. a bank export.

## 1. Setup (onboarding)

1. Open Arca. You should see **Welcome to Arca** with the privacy note.
2. **Folders**: add `~/ArcaTest`. Continue. (The server restarts here, which
   takes a second or two.)
3. **Model**: pick Llama 3.2 or Qwen 2.5 and click **Download**. The search
   model (80 MB) downloads first, then the language model (~2 GB). Try
   **Cancel** once and start again if you like.
4. **You're all set**: the line under the title shows indexing progress.

Expected: no step is stuck, errors are explained, and the main window opens.

## 2. Ask Documents

Ask something only one of your documents answers ("What is the monthly
rent?"). Expected:

- a "Searching…/Thinking…" indicator, then a streamed answer,
- **Sources** chips under the answer; clicking one opens the file,
- the stop button stops a long answer.

The first answer after launch is slower because the model loads (a few
seconds on Apple Silicon).

## 3. Inbox (suggestions for new files)

1. Copy a new document into `~/ArcaTest` (not into a sub-folder), e.g. another
   flight booking.
2. Within ~10–30 seconds the **Inbox** badge shows 1.
3. The suggestion should name the matching sub-folder (`Travel`). Try
   **Move**, then **Undo** under "Recently moved". Try **Skip** and
   **⋯ → Choose Another Folder…** on other files.

## 4. Tidy a Folder

Choose `~/ArcaTest`, click **Suggest Folders**, untick a move or two, and
click **Move**. Only loose files directly in the folder are proposed.

## 5. Ask Spreadsheets

Click **Import** (or drop the CSV/Excel onto the window). Ask e.g. "Total per
category" or "Largest expense in March". Open **Query** to see the SQL that
ran.

## 6. Settings (⌘,)

- Change a folder's mode between Suggest and Off.
- Add/remove a folder (the server restarts; the status at the bottom-left
  of the sidebar shows it).
- Switch models if you downloaded both.
- **Show Server Log** opens the log if anything went wrong.

## Feedback that helps most

- Anything that looked stuck, confusing, or wrong, with a screenshot.
- Wrong answers in Ask Documents/Spreadsheets: the question, the answer, and
  (for spreadsheets) the SQL shown under **Query**.
- Bad Inbox suggestions: the file and where it should have gone.
- If something failed: `~/Library/Logs/Arca/arca-server.log`.

## Known limitations

- Not notarized: the first launch needs the Gatekeeper step above.
- Apple Silicon only.
- Only `.pdf`, `.docx`, `.xlsx`, `.md`, `.txt` and `.csv` are read for
  search; scanned PDFs (images) have no text to read.
- Watched folders' sub-folders are searched, but only files arriving
  directly in a watched folder get Inbox suggestions.
- Moves made with Tidy a Folder are recorded but not listed in the Inbox for
  undo.
