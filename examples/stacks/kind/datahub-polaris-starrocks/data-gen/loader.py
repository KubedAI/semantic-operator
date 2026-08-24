"""Create the demo tables in a StarRocks Iceberg (REST-catalog) external
catalog and bulk-load the generated rows.

It is deliberately minimal: the REST catalog (Polaris) owns table locations, so
there is no S3 preflight, explicit LOCATION, manifest, or force/reset mode.
Before any table creation or INSERT, it verifies that the namespace is empty or
that the complete expected dataset is already present. Partial data is never
appended.
"""

import re
import time

import constants
import generators
import schema

# DEFAULT_BATCH_SIZE is the number of rows per INSERT statement.
DEFAULT_BATCH_SIZE = 1000

_VERIFY_POLL_INTERVAL = 2.0     # seconds
_VERIFY_TIMEOUT = 90.0          # seconds

_IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class LoaderError(Exception):
    """A database load failure that carries table/operation context.

    The Go command it replaces wrapped every database error with the operation
    and table that failed (for example `loader: create table account: ...`).
    This type carries the same context so the executable can report one concise
    line without a traceback.
    """


class Loader:
    """Loads the generated tables through a DB-API connection (e.g. PyMySQL).

    The connection must be in autocommit mode: StarRocks commits each table to
    Iceberg as it is inserted.
    """

    def __init__(self, conn, catalog, database, batch_size=DEFAULT_BATCH_SIZE, log=None):
        catalog = (catalog or "").strip()
        database = (database or "").strip() or constants.DATABASE_NAME
        if not _IDENTIFIER.match(catalog):
            raise ValueError(f"loader: invalid or empty catalog {catalog!r}")
        if not _IDENTIFIER.match(database):
            raise ValueError(f"loader: invalid database {database!r}")
        if batch_size == 0:
            batch_size = DEFAULT_BATCH_SIZE
        if batch_size < 1 or batch_size > 5000:
            raise ValueError(f"loader: batch size must be between 1 and 5000, got {batch_size}")
        self._conn = conn
        self._catalog = catalog
        self._database = database
        self._batch_size = batch_size
        self._log = log or (lambda *_a: None)

    # --- SQL helpers --------------------------------------------------------

    def _exec(self, query, args=None, context=None):
        try:
            with self._conn.cursor() as cur:
                cur.execute(query, args or [])
        except Exception as err:  # noqa: BLE001 - add operation context, report once
            raise LoaderError(f"{context}: {err}" if context else str(err)) from err

    def _query(self, query, args=None, context=None):
        try:
            with self._conn.cursor() as cur:
                cur.execute(query, args or [])
                return cur.fetchall() or []
        except Exception as err:  # noqa: BLE001 - add operation context, report once
            raise LoaderError(f"{context}: {err}" if context else str(err)) from err

    def _database_name(self):
        return f"`{self._catalog}`.`{self._database}`"

    def _fq(self, table):
        return f"`{self._catalog}`.`{self._database}`.`{table}`"

    # --- Run ----------------------------------------------------------------

    def run(self):
        """Create the database, check the whole dataset state, then create and
        load tables only when every existing expected table is empty. A complete
        dataset is skipped. Partial or mismatched data fails before any INSERT.
        """
        self._exec("CREATE DATABASE IF NOT EXISTS " + self._database_name(),
                   context=f"loader: create database {self._database_name()}")
        if self._preflight_existing_data():
            self._log("dataset already loaded in %s; skipping", self._database_name())
            return
        for table in generators.tables():
            self._exec(self._create_table_sql(table),
                       context=f"loader: create table {table}")
            self._insert_rows(table)
            expected = constants.EXPECTED_TABLE_ROW_COUNTS[table]
            count = self._verify_exact_count(table, expected)
            self._log("loaded %-32s %d rows", table, count)

    def _preflight_existing_data(self):
        """Return True only when the complete expected dataset is already
        present. Loading into an empty namespace (including empty expected
        tables) is permitted; appending to any partial or mismatched dataset is
        refused.
        """
        tables = generators.tables()
        expected = set(tables)

        rows = self._query("SHOW TABLES FROM " + self._database_name(),
                           context=f"loader: list existing tables in {self._database_name()}")
        existing = set()
        unexpected = []
        for row in rows:
            if len(row) != 1:
                raise ValueError(
                    f"loader: list existing tables in {self._database_name()} "
                    f"returned an unexpected result")
            table = _scalar_string(row[0])
            if not table:
                raise ValueError(
                    f"loader: list existing tables in {self._database_name()} "
                    f"returned an empty table name")
            if table in existing:
                continue
            existing.add(table)
            if table not in expected:
                unexpected.append(table)
        if unexpected:
            unexpected.sort()
            raise ValueError(
                f"loader: unexpected tables in {self._database_name()}: "
                f"{', '.join(unexpected)}; refusing to modify this namespace")
        if not existing:
            return False

        counts = {}
        non_empty = False
        complete = len(existing) == len(tables)
        for table in tables:
            if table not in existing:
                complete = False
                continue
            try:
                count = self._count_table(table)
            except LoaderError as err:
                raise LoaderError(f"loader: inspect existing table {table}: {err}")
            counts[table] = count
            if count > 0:
                non_empty = True
            if count != constants.EXPECTED_TABLE_ROW_COUNTS[table]:
                complete = False
        if complete:
            return True
        if not non_empty:
            return False

        issues = []
        for table in tables:
            if table not in counts:
                issues.append(table + "=missing")
                continue
            expected_count = constants.EXPECTED_TABLE_ROW_COUNTS[table]
            if counts[table] != expected_count:
                issues.append(f"{table}={counts[table]} (expected {expected_count})")
        raise ValueError(
            f"loader: existing dataset in {self._database_name()} is partial or has "
            f"mismatched row counts: {', '.join(issues)}; refusing to append; drop the "
            f"demo tables before retrying")

    def _create_table_sql(self, table):
        columns = schema.schema(table)
        if not columns:
            raise ValueError(f"loader: no schema for table {table!r}")
        parts = []
        for column in columns:
            nullability = "" if column.nullable else " NOT NULL"
            parts.append(f"`{column.name}` {column.type}{nullability}")
        return f"CREATE TABLE IF NOT EXISTS {self._fq(table)} ({', '.join(parts)})"

    def _insert_rows(self, table):
        columns = schema.schema(table)
        column_list = ",".join(f"`{c.name}`" for c in columns)
        one = "(" + ",".join(["%s"] * len(columns)) + ")"
        for batch in generators.rows(table, self._batch_size):
            placeholders = []
            args = []
            for row in batch:
                if len(row) != len(columns):
                    raise ValueError(
                        f"loader: {table} row has {len(row)} values, want {len(columns)}")
                placeholders.append(one)
                args.extend(row)
            query = (f"INSERT INTO {self._fq(table)} ({column_list}) "
                     f"VALUES {','.join(placeholders)}")
            self._exec(query, args, context=f"loader: insert {table} batch")

    def _count_table(self, table):
        rows = self._query("SELECT COUNT(*) FROM " + self._fq(table))
        if len(rows) != 1 or len(rows[0]) != 1:
            raise ValueError(f"loader: count {table} returned an unexpected result")
        return _scalar_int(rows[0][0])

    def _verify_exact_count(self, table, expected):
        """Poll COUNT(*) until it equals expected or the timeout elapses. The
        short retry tolerates the brief lag between an Iceberg commit and its
        visibility through the external catalog.
        """
        deadline = time.monotonic() + _VERIFY_TIMEOUT
        last = 0
        observed = False
        while True:
            try:
                count = self._count_table(table)
            except LoaderError as err:
                raise LoaderError(f"loader: verify {table} row count: {err}")
            last, observed = count, True
            if count == expected:
                return count
            if time.monotonic() >= deadline:
                seen = str(last) if observed else "none"
                raise ValueError(
                    f"loader: verify {table} row count did not converge: "
                    f"last observed {seen}, expected {expected}")
            time.sleep(_VERIFY_POLL_INTERVAL)


def _scalar_int(value):
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return value
    if isinstance(value, bytes):
        return int(value.decode("utf-8"))
    return int(str(value).strip())


def _scalar_string(value):
    if isinstance(value, bytes):
        return value.decode("utf-8").strip()
    return str(value).strip()
