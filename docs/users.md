# Users and devices

English | [Русский](ru/users.md)

Every person has one **user** with a **panel password** and any number of **devices**, one per
e-reader. Each device has its own password, which KOReader uses for progress sync, statistics
and the OPDS catalog. The two kinds of passwords are separate: a device password does not open
the panel, and the panel password does not work in KOReader.

## In the panel

Every user manages their own devices under **Devices**: add a device (its password is shown
once), issue a new password, revoke. The panel password is changed under **Account**, which
signs out the user's other sessions.

Administrators manage users under **Users** (`/admin/users`, on a phone via **Account**):
create a user, issue a new panel password, change the role, disable or enable, remove the OIDC
link, delete. New and reset passwords are shown once. An administrator cannot apply these
actions to their own account, so the panel cannot lock out its last administrator.

A panel session lasts 30 days from the last use. The session cookie gets the `Secure` flag when
the panel is opened over HTTPS (directly or through a proxy that sends
`X-Forwarded-Proto: https`); over plain `http://` in a local network it works without it.

## Command line

Everything is also available in the `ksb` CLI, which has none of the panel's checks: it is the
recovery path when nobody can sign in. It works on the same database while the server is
running, and changes (for example, a revoked device) apply on the next request. With the binary,
run `ksb` instead of `docker exec ksb /ksb` (see [Installation](installation.md#binary)).

```sh
docker exec ksb /ksb user add alice              # [-role admin|user]; no panel password yet
docker exec ksb /ksb user passwd alice           # prints a new panel password once
docker exec ksb /ksb user list
docker exec ksb /ksb user role alice admin       # or user
docker exec ksb /ksb user disable alice          # blocks all of alice's devices and panel sessions
docker exec ksb /ksb user enable alice           # devices work again, old panel sessions do not
docker exec ksb /ksb user delete alice           # removes devices, reading progress and statistics
docker exec ksb /ksb user oidc-unlink alice      # removes the OIDC link; `user list` shows links

docker exec ksb /ksb device add alice "Kindle"   # prints the device password once
docker exec ksb /ksb device list alice           # names, creation and last use, revoked flag
docker exec ksb /ksb device revoke alice "Kindle"
```

- `user passwd` prints a generated password `xxxx-xxxx-xxxx-xxxx` and signs the user out of all
  panel sessions; device passwords stay as they are.
- `device add` prints a password of the form `xxxx-xxxx` (8 random characters, easy to type on
  an e-reader keyboard). It is not stored in readable form and cannot be shown again.
- After `ksb user enable`, an e-reader that was refused while the user was disabled may get
  refused for up to 10 more minutes: the running server remembers rejected keys and the CLI is
  a separate process. Enabling the user in the panel applies immediately.
