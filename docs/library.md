# Library and OPDS

English | [Русский](ru/library.md)

- [Folders](#folders)
- [Uploading books](#uploading-books)
- [Book metadata](#book-metadata)
- [FB2 to EPUB](#fb2-to-epub)
- [OPDS catalog](#opds-catalog)

## Folders

The server can index folders with book files. A book synced without **Send document metadata**
and without statistics then gets its title, authors and file name from its file: the server
matches the synced document hash to the file by the same partial MD5 KOReader computes
(*Binary* matching) or by the MD5 of the file name (*Filename* matching, the CrossPoint
default). Titles from the sync metadata and from the user's statistics win over the file's
metadata. Metadata edited in the library (by hand or from a catalog) wins over everything: the
home page and the book list show it.

An administrator adds folders on the **Library** screen of the panel. The path is an absolute
path on the server; the server only reads it and never writes there. Mount the folder read-only
and enter the path inside the container:

```sh
docker run -d --name ksb \
  -v ksb-data:/data \
  -v /srv/books:/books:ro \
  -p 8080:8080 \
  ghcr.io/varrcan/koreader-sync-board:latest
```

The container user `65532` (or the service user of the binary) needs read access to the files
and directories.

- **Formats**: `.epub`, `.fb2`, `.fb2.zip`, `.pdf`, `.djvu`, `.mobi`, `.azw`, `.cbz`. Titles,
  authors, series and language are read from FB2 (`<title-info>`, including single-byte
  encodings such as windows-1251) and EPUB (the OPF package). Other formats are matched by hash
  and file name only. Files and folders starting with a dot and `*.sdr` folders are skipped;
  symbolic links inside a folder are not followed.
- **Grouping**: files with the same normalized title and authors (for example the FB2 and the
  EPUB of one book) form one library book.
- **Scanning** runs on start, every `KSB_LIBRARY_SCAN_INTERVAL` (6 hours by default), after a
  folder is added and with the **Scan** button. Unchanged files (same size and modification
  time) are not read again.
- **Covers**: after walking the folders, the scanner reads the cover of each FB2, FB2.ZIP and
  EPUB file once (and again when the file changes) and keeps a JPEG thumbnail in `covers/`
  inside the data directory; nothing is written to the book folders. Other formats and books
  without a cover get a generated one.
- **Unavailable storage**: if a folder cannot be read or is suddenly empty (an unmounted NAS),
  its index is kept and the panel shows the error. To clear the index of a folder emptied on
  purpose, delete the folder in the panel.
- **NFS**: mount with `soft` and a `timeo`: on a hard mount a dead server blocks the scan until
  it comes back, and other folders are not scanned meanwhile.

## Uploading books

Books can be added without access to the server: upload them in the panel or put them into the
WebDAV folder. Uploaded files are kept in `uploads/` inside the data directory and are indexed
like files of any folder: hashes, metadata, grouping, covers. Any user can upload books and delete
uploaded files: the library is shared.

- **In the panel**: the **Upload books** button on the **Library** screen, or drop files onto the
  page. Files go one at a time. Unsupported formats, empty files and files larger than 1 GiB are
  rejected. If the library already has a file with the same content (the same partial MD5), the
  new one is not saved and the panel links to that book. If the name is taken, ` (2)` is added.
- **Over WebDAV**: address `https://<host>/dav/books/`, your username and a **device password**,
  as for statistics. Subfolders, renaming and deleting work; a book put into the folder shows up
  in the library within seconds. Tested with `rclone` and GNOME Files (gvfs); Finder and Windows
  Explorer have not been tested. Windows only mounts WebDAV with a password over HTTPS by
  default. An rclone example:

  ```sh
  rclone copy ~/Books ":webdav,url='https://read.example.org/dav/books',vendor=other,user=alice,pass=$(rclone obscure <device password>):"
  ```

  Anything can be stored in the folder, but only book files get into the library. Duplicates are
  not checked over WebDAV.
- **Deleting**: **Delete** next to an uploaded file on the book page removes it from the library
  and from disk. Reading progress is kept. Files of server folders cannot be deleted from the
  panel.
- **Proxy**: request bodies go up to 1 GiB, raise the reverse proxy limit (see
  [security](security.md)).

## Book metadata

An administrator can correct a library book with **Metadata** on its page: pick a match found in
the book's files or in an online catalog, review the form and save, or fill the form by hand.
Edits are stored in the database, on top of what the files say: the files are never changed, and
a rescan does not overwrite them. An empty field hides the value from the file. A field equal to
the file's value is not stored, so it keeps following the file. **Reset to file data** removes
all edits of the book.

- **Catalogs**: FantLab and Open Library work without configuration. Google Books is enabled by
  `KSB_GOOGLE_BOOKS_API_KEY`. The search looks up the ISBN from the book's files first, then the
  title and authors.
- **Network**: the server contacts catalogs only when an administrator opens the dialog or
  searches. It needs outbound HTTPS to `api.fantlab.ru`, `fantlab.ru`, `openlibrary.org`,
  `covers.openlibrary.org`, `archive.org` and its subdomains and, with a key,
  `www.googleapis.com`, `books.google.com` and `*.googleusercontent.com`. If a catalog is
  unreachable, the dialog says so and shows the others.
- **Cache**: FantLab and Open Library answers are kept in the database for 7 days. Google Books
  answers are not cached.
- **Google Books terms**: saving a Google Books match stores its data in your database for as
  long as the edit exists. Complying with the
  [Google APIs Terms of Service](https://developers.google.com/terms) is up to the owner of the
  key.

## FB2 to EPUB

The **To EPUB** button on a book page converts an FB2 or FB2.ZIP file. The EPUB joins the same
book and is stored in `converted/` inside the data directory, never in the book folders. It
works like any other library file (hashes, progress, download), and its bytes never change on
their own, even if the FB2 or the converter does, because new bytes would mean a new document
hash. An admin can delete the result and convert again; reading progress stays. One conversion
runs at a time, in a separate process, for up to 5 minutes.

## OPDS catalog

The library is also available as an OPDS 1.2 catalog at `/opds`, for KOReader, CrossPoint and
other OPDS readers. It offers every book newest first, search, all files of a book with their
format and size, and covers. A downloaded file is byte-for-byte the library file, so progress
syncs against the same book. A book that has an FB2 but no EPUB also gets an EPUB link: the file
is converted on the first download and kept, like a conversion from the panel.

In KOReader: the search tab of the file manager → **OPDS catalog** → menu in the title bar →
**Add catalog**, with the URL `https://<host>/opds`, your username and a **device password**
(not the panel password). Revoking the device closes the catalog on it. Without a library the
catalog is empty.

The catalog uses HTTP Basic, so over plain `http://` the device password travels unencrypted:
expose it through a TLS reverse proxy, as you would the WebDAV endpoint.
