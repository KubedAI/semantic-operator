# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "PyMySQL==1.1.1",
# ]
# ///
"""Generate and load the local SaaS accounts dataset."""

import argparse
import sys

import constants
import generators
import loader


def parse_args(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1", help="StarRocks FE host")
    parser.add_argument("--port", type=int, default=9030, help="StarRocks FE MySQL port")
    parser.add_argument("--user", default="root", help="StarRocks username")
    parser.add_argument("--password", default="", help="StarRocks password")
    parser.add_argument(
        "--catalog", default="iceberg", help="StarRocks Iceberg catalog name"
    )
    parser.add_argument(
        "--database",
        default=constants.DATABASE_NAME,
        help="Iceberg namespace or database",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=loader.DEFAULT_BATCH_SIZE,
        help="rows per INSERT (1..5000)",
    )
    parser.add_argument(
        "--count-only",
        action="store_true",
        help="generate all tables and print row counts without connecting",
    )
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    if args.count_only:
        return print_counts()

    connection = None
    try:
        connection = open_starrocks(args.host, args.port, args.user, args.password)
        data_loader = loader.Loader(
            connection,
            catalog=args.catalog,
            database=args.database,
            batch_size=args.batch_size,
            log=log,
        )
        log(
            "loading small dataset into `%s`.`%s` via %s:%d",
            args.catalog,
            args.database,
            args.host,
            args.port,
        )
        data_loader.run()
        log("done")
        return 0
    except Exception as error:
        print(f"data-gen: {error}", file=sys.stderr)
        return 1
    finally:
        if connection is not None:
            connection.close()


def open_starrocks(host, port, user, password):
    if not host:
        raise ValueError("--host is required")
    if port < 1 or port > 65535:
        raise ValueError(f"invalid port {port}")

    import pymysql

    try:
        return pymysql.connect(
            host=host,
            port=port,
            user=user,
            password=password,
            autocommit=True,
            connect_timeout=15,
            read_timeout=600,
            write_timeout=600,
        )
    except Exception as error:
        raise RuntimeError(f"connect to StarRocks at {host}:{port}: {error}") from error


def print_counts():
    total = 0
    valid = True
    for table in generators.tables():
        count = sum(len(batch) for batch in generators.rows(table, 4096))
        expected = constants.EXPECTED_TABLE_ROW_COUNTS[table]
        status = "ok" if count == expected else f"MISMATCH (want {expected})"
        print(f"{table:<32} {count:>8}  {status}")
        total += count
        valid = valid and count == expected
    print(f"{'TOTAL':<32} {total:>8}")
    return 0 if valid else 1


def log(message, *args):
    print(message % args if args else message, file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
