# Connecting KOReader

English | [Русский](ru/koreader.md)

Add a device for the e-reader first: in the panel under **Devices**, or with
`ksb device add <user> <device>` (see [Users and devices](users.md)). The panel's **Help**
screen shows the same steps with your server's address filled in.

## Progress sync

1. Open a book, then **Tools → Progress sync → Custom sync server** and enter
   `http://<host>:8080`.
2. **Register / Login** → enter the username and the **device password**, then choose
   **Login**. Registration from the device is disabled by default; see
   `KSB_KOSYNC_REGISTRATION` in [Configuration](configuration.md).
3. Optional, KOReader v2026.07 and later: enable **Send document metadata** so the server
   receives the filename, title and authors with each update. Without it the server only knows
   the document hash.

Both document matching methods (*Binary* and *Filename*) are supported.

## Reading statistics

KOReader sends its reading statistics (`statistics.sqlite3`: time and pages per book) through
its built-in cloud sync over WebDAV. The server accepts it at `/dav` with the same device
password; no plugin is needed.

1. **Cloud storage** → add a **WebDAV** server: address `http://<host>:8080/dav`, the username
   and the **device password**. Leave the start folder empty. KOReader 2026.07 has two such menu
   items: use **Cloud storage+**. The older **Cloud storage** app keeps its own list of servers,
   and statistics sync does not see it.
2. **Reading statistics → Settings → Cloud sync** → pick this server, then in the folder list
   long-press **Long-press here to choose current folder**.
3. **Reading statistics → Sync now**. Repeat whenever you want to sync; KOReader has no timer.

Several e-readers of one user merge their statistics through the server: KOReader downloads the
file, merges it with its own and uploads the result. The vocabulary builder
(`vocabulary_builder.sqlite3`) syncs the same way and is stored as is. Files are limited to
64 MiB.

- Use a password issued by the panel or `ksb device add`. KOReader URL-decodes the WebDAV
  password, so a password containing `%` (possible for one set from KOReader with **Register**
  or a password change) works for progress sync but not for WebDAV.
- The panel shows when statistics were last synced and can upload a `statistics.sqlite3` by
  hand (for example, from an old e-reader). The upload is merged into the statistics on the
  server without deleting anything, and readers get it on their next sync.
- Books synced without **Send document metadata** get their title and authors from the
  statistics.

## OPDS catalog

With a [library](library.md) configured, add the catalog in KOReader: the search tab of the
file manager → **OPDS catalog** → menu in the title bar → **Add catalog**, with the URL
`https://<host>/opds`, the username and a **device password**.

## Over the internet

Progress sync, WebDAV and OPDS send the device password with every request. Outside the home
network, put the server behind a TLS reverse proxy: see [Security](security.md).
