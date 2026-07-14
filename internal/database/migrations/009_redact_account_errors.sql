UPDATE accounts
SET last_error = 'redacted upstream error'
WHERE last_error ~* '(https?://|authorization|bearer[[:space:]]|access[-_ ]token|refresh[-_ ]token|id[-_ ]token|sso=)';

UPDATE accounts
SET last_error = LEFT(last_error, 256)
WHERE CHAR_LENGTH(last_error) > 256;
