-- Привязка пользователя к учётке OIDC-провайдера. sub уникален только в пределах
-- issuer, поэтому хранится пара: смена KSB_OIDC_ISSUER не должна пускать чужих
-- людей в старые учётки. Провайдер один, привязка у пользователя максимум одна.
ALTER TABLE users ADD COLUMN oidc_issuer  TEXT;
ALTER TABLE users ADD COLUMN oidc_subject TEXT;
CREATE UNIQUE INDEX users_oidc ON users (oidc_issuer, oidc_subject)
    WHERE oidc_subject IS NOT NULL;
