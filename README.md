# Joka

Joka is a small tool for managing migrations in MySQL. It's very early release so may change, and is built to suit my specific requirements, so I don't expect it will support much more than it does.

<p align="center">
  <img src="joka.jpg" alt="joka" width="400">
</p>

## Install

Not much going now, but I suggest using the releases and pipx. Current latest release:

```
pipx install https://github.com/apsdsm/joka/releases/download/v0.1.1/joka-0.1.1-py3-none-any.whl
```

## Setup

### Database Path

Either add a .env file in the directory you run from, or ensure that a DATABASE_URL environment variable is available at run time. You can specify an .env file to load using the `--env` parameter.

The DATABASE_URL param should have the whole connection string, as in:

```
DATABASE_URL=mysql+asyncmy://name:pass@localhost:3306/my_db
```

### Migration Files

Put your migrations into a single folder, and make sure they all have the naming pattern `YYMMDDHHMMSS_description.sql`, as in: `2512251524_add_jinglebells.sql`. Each file should contain a complete SQL statement. The file contents will be executed according to the order defined by that first date/time part of the string.

### Initialize Joka

You need to initialize Joka at least once to make the migrations table. Run `joka init` and it will try to make a table called `migrations` in your db.

## Commands

Run `joka --help` for more info.

### Up

Will show you the current status of the db, and if given permission will apply any pending migrations. Will update the migrations table to keep track of what was applied.

### Status

Will show you the current status of the db, but not try apply anything.

### Init

Will create a migrations table in the database.
