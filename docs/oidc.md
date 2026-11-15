# Signing in with OIDC

English | [Русский](ru/oidc.md)

The panel can sign users in through one OpenID Connect provider (Zitadel, Keycloak, Authentik …)
with the Authorization Code flow and PKCE. KOReader is not affected: e-readers keep using device
passwords. OIDC is enabled by three variables:

```sh
docker run ... \
  -e KSB_OIDC_ISSUER=https://id.example.com \
  -e KSB_OIDC_CLIENT_ID=123456789@books \
  -e KSB_PUBLIC_URL=https://books.example.com \
  -e KSB_OIDC_NAME=Zitadel \
  ghcr.io/varrcan/koreader-sync-board:latest
```

All OIDC variables are listed in [Configuration](configuration.md#oidc). Register this
**redirect URI** with the provider:

```
https://books.example.com/api/v1/auth/oidc/callback
```

`KSB_PUBLIC_URL` is the address users open the panel at: an origin only (`https://host` or
`http://host:port`), without a path — serving the panel under a path prefix is not supported. If
someone opens the panel by another address (a LAN IP, `localhost`), the sign-in button first
sends them to `KSB_PUBLIC_URL` and the whole sign-in happens there; linking an account (see
below) only works at the public address, and the panel shows a link to it.

The server does not contact the provider on startup: discovery happens on the first sign-in, and
if the provider is down the sign-in fails with a message and is retried next time, while the
server and KOReader sync keep working.

## Who gets in

A panel user is tied to the provider account by its issuer and `sub`, never by name or email.
There are two ways to tie them:

- an existing user signs in with the panel password, opens **Account** and clicks **Link**;
  **Unlink** removes the link (allowed only when the user has a panel password);
- with `KSB_OIDC_AUTO_CREATE=true`, the first sign-in of an unknown provider account creates a
  panel user named after the `preferred_username` claim (`KSB_OIDC_USERNAME_CLAIM`), with no
  panel password and no devices. If that name is already taken, the sign-in is rejected and the
  existing user stays untouched — sign in as that user with the password and link instead. A
  user created this way can set a panel password under **Account**.

The first administrator is still created by the setup wizard or `ADMIN_USER`/`ADMIN_PASSWORD`;
before that, OIDC sign-in is refused. Who may sign in at all is decided by the provider (for
example, which users are granted the project in Zitadel).

## Admin role from a claim

With `KSB_OIDC_ADMIN_CLAIM` and `KSB_OIDC_ADMIN_VALUE` set, every OIDC sign-in (and linking)
makes the user `admin` if the claim in the ID token matches and `user` otherwise; the change is
logged. A match is a string equal to the value, an array containing it, or an object with it as
a key. Without these variables OIDC never changes roles. If the provider dropped the role by
mistake, restore it with `ksb user role alice admin` (the next OIDC sign-in applies the claim
again).

## Turning off password sign-in

`KSB_PANEL_PASSWORD_LOGIN=false` (only together with OIDC) disables the panel password for users
**linked** to the provider; users without a link still sign in with their password, so you
cannot lock yourself out. A safe switch:

1. create the administrator with the setup wizard or `ADMIN_USER`/`ADMIN_PASSWORD`;
2. sign in with the password and link the provider account under **Account**;
3. restart with `KSB_PANEL_PASSWORD_LOGIN=false`.

Signing out of the panel ends only the panel session, not the session at the provider; disabling
a user at the provider does not end their open panel sessions — use `ksb user disable` for that.

## Changing the issuer

Links are tied to the issuer URL. After `KSB_OIDC_ISSUER` changes (a new domain for the
provider), old links no longer match: remove them with `ksb user oidc-unlink alice` and link
again.

## Zitadel

1. In a project, create an application of type **Web**: authentication method **PKCE** (no
   secret) or **Code** (then set `KSB_OIDC_CLIENT_SECRET`). Add the redirect URI above.
2. In the application's token settings enable **User Info inside ID Token** (the username comes
   from the ID token) and, for the role claim, **User roles inside ID Token**.
3. For the role claim, enable **Assert Roles on Authentication** in the project, create a role
   (for example `admin`) and grant it to the administrators. Then set

   ```sh
   KSB_OIDC_ADMIN_CLAIM=urn:zitadel:iam:org:project:roles
   KSB_OIDC_ADMIN_VALUE=admin
   ```

`KSB_OIDC_ISSUER` is the instance URL, for example `https://id.example.com`; the client ID is
shown on the application page. For Keycloak or Authentik a `groups` claim works the same way:
`KSB_OIDC_ADMIN_CLAIM=groups`, `KSB_OIDC_ADMIN_VALUE=ksb-admins`.
