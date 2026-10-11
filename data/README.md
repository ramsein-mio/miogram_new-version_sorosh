Truth/dare question database
=============================

The bot reads random truth/dare questions exclusively from `data/jh.db`. It does
not read or seed a PostgreSQL question table. The database uses this schema:

```sql
CREATE TABLE questions (
    id       INTEGER PRIMARY KEY,
    kind     TEXT NOT NULL CHECK (kind IN ('truth', 'dare')),
    question TEXT NOT NULL,
    enabled  INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    UNIQUE(kind, question)
);
```

Use `kind='truth'` for truth questions and `kind='dare'` for dares. Set `enabled=0`
to temporarily disable a question. `data/jh.db` is included with test questions so
the game can be tried immediately; replace or extend these rows with your own pool.

The Docker image installs the `sqlite3` command-line tool used to query this file.
The application only needs read access to the database at runtime.
