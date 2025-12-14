# Joka

Joka is a small tool for managing migrations in MySQL. It's very early release so may change, and is built to suit my specific requirements, so I don't expect it will support much more than it does.

```
DATABASE_URL=mysql+asyncmy://root:root@localhost:3306/penpal
```


## Setup
Install the command using 


- Clone the repo and install dependencies:
  - With uv: `uv sync`
  - With pip: `python -m venv .venv && source .venv/bin/activate && pip install -e .`
- Create an `.env` file with at least:
  ```
  DATABASE_URL=mysql+asyncmy://user:pass@host:3306/dbname
  ```
- Create your migrations directory if it does not exist (default `devops/db/migrations`).

## Writing migrations
- File naming pattern: `YYMMDDHHMMSS_description.sql` (example: `20250101010101_create_users.sql`).
- Each file contains raw SQL; statements are applied in filename order.
- A `migrations` table is created in your database to track what has been applied.

## CLI usage
All commands share global options:
- `--env`: Path to the env file (default `.env`).
- `--migrations`: Path to migrations directory (default `devops/db/migrations`).
- `--auto`: Automatically confirm prompts (intended to bypass confirmations).

Common commands (run from repo root):
- Initialize tracking table:  
  `python joka.py --env .env --migrations devops/db/migrations init`
- Show migration chain/status:  
  `python joka.py --env .env --migrations devops/db/migrations status`
- Apply pending migrations (prompts before applying):  
  `python joka.py --env .env --migrations devops/db/migrations up`

## Notes / TODO
- The filesystem helpers for parsing migration file contents are still stubs.
- The CLI currently assumes MySQL/MariaDB semantics (uses `information_schema` and `AUTO_INCREMENT`).
