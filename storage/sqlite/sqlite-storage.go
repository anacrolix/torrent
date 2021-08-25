//go:build cgo
// +build cgo

package sqliteStorage

import (
	_ "embed"
	"errors"
	"fmt"
	"net/url"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

type conn = *sqlite.Conn

type InitConnOpts struct {
	SetSynchronous int
	SetJournalMode string
	MmapSizeOk     bool  // If false, a package-specific default will be used.
	MmapSize       int64 // If MmapSizeOk is set, use sqlite default if < 0, otherwise this value.
}

type UnexpectedJournalMode struct {
	JournalMode string
}

func (me UnexpectedJournalMode) Error() string {
	return fmt.Sprintf("unexpected journal mode: %q", me.JournalMode)
}

func setSynchronous(conn conn, syncInt int) (err error) {
	err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma synchronous=%v`, syncInt), nil)
	if err != nil {
		return err
	}
	var (
		actual   int
		actualOk bool
	)
	err = sqlitex.ExecTransient(conn, `pragma synchronous`, func(stmt *sqlite.Stmt) error {
		actual = stmt.ColumnInt(0)
		actualOk = true
		return nil
	})
	if err != nil {
		return
	}
	if !actualOk {
		return errors.New("synchronous setting query didn't return anything")
	}
	if actual != syncInt {
		return fmt.Errorf("set synchronous %q, got %q", syncInt, actual)
	}
	return nil
}

func initConn(conn conn, opts InitConnOpts) (err error) {
	err = sqlitex.ExecTransient(conn, "pragma foreign_keys=on", nil)
	if err != nil {
		return err
	}
	err = setSynchronous(conn, opts.SetSynchronous)
	if err != nil {
		return
	}
	// Recursive triggers are required because we need to trim the blob_meta size after trimming to
	// capacity. Hopefully we don't hit the recursion limit, and if we do, there's an error thrown.
	err = sqlitex.ExecTransient(conn, "pragma recursive_triggers=on", nil)
	if err != nil {
		return err
	}
	if opts.SetJournalMode != "" {
		err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma journal_mode=%s`, opts.SetJournalMode), func(stmt *sqlite.Stmt) error {
			ret := stmt.ColumnText(0)
			if ret != opts.SetJournalMode {
				return UnexpectedJournalMode{ret}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if !opts.MmapSizeOk {
		// Set the default. Currently it seems the library picks reasonable defaults, especially for
		// wal.
		opts.MmapSize = -1
		//opts.MmapSize = 1 << 24 // 8 MiB
	}
	if opts.MmapSize >= 0 {
		err = sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma mmap_size=%d`, opts.MmapSize), nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func setPageSize(conn conn, pageSize int) error {
	if pageSize == 0 {
		return nil
	}
	var retSize int64
	err := sqlitex.ExecTransient(conn, fmt.Sprintf(`pragma page_size=%d`, pageSize), nil)
	if err != nil {
		return err
	}
	err = sqlitex.ExecTransient(conn, "pragma page_size", func(stmt *sqlite.Stmt) error {
		retSize = stmt.ColumnInt64(0)
		return nil
	})
	if err != nil {
		return err
	}
	if retSize != int64(pageSize) {
		return fmt.Errorf("requested page size %v but got %v", pageSize, retSize)
	}
	return nil
}

var (
	//go:embed init.sql
	initScript string
	//go:embed init-triggers.sql
	initTriggers string
)

func InitSchema(conn conn, pageSize int, triggers bool) error {
	err := setPageSize(conn, pageSize)
	if err != nil {
		return fmt.Errorf("setting page size: %w", err)
	}
	err = sqlitex.ExecScript(conn, initScript)
	if err != nil {
		return err
	}
	if triggers {
		err := sqlitex.ExecScript(conn, initTriggers)
		if err != nil {
			return err
		}
	}
	return nil
}

type InitDbOpts struct {
	DontInitSchema bool
	PageSize       int
	// If non-zero, overrides the existing setting.
	Capacity   int64
	NoTriggers bool
}

// Remove any capacity limits.
func unlimitCapacity(conn conn) error {
	return sqlitex.Exec(conn, "delete from setting where key='capacity'", nil)
}

// Set the capacity limit to exactly this value.
func setCapacity(conn conn, cap int64) error {
	return sqlitex.Exec(conn, "insert into setting values ('capacity', ?)", nil, cap)
}

type NewConnOpts struct {
	// See https://www.sqlite.org/c3ref/open.html. NB: "If the filename is an empty string, then a
	// private, temporary on-disk database will be created. This private database will be
	// automatically deleted as soon as the database connection is closed."
	Path   string
	Memory bool
	// Whether multiple blobs will not be read simultaneously. Enables journal mode other than WAL,
	// and NumConns < 2.
	NoConcurrentBlobReads bool
}

func newOpenUri(opts NewConnOpts) string {
	path := url.PathEscape(opts.Path)
	if opts.Memory {
		path = ":memory:"
	}
	values := make(url.Values)
	if opts.NoConcurrentBlobReads || opts.Memory {
		values.Add("cache", "shared")
	}
	return fmt.Sprintf("file:%s?%s", path, values.Encode())
}

func initDatabase(conn conn, opts InitDbOpts) (err error) {
	if !opts.DontInitSchema {
		err = InitSchema(conn, opts.PageSize, !opts.NoTriggers)
		if err != nil {
			return
		}
	}
	if opts.Capacity < 0 {
		err = unlimitCapacity(conn)
	} else if opts.Capacity > 0 {
		err = setCapacity(conn, opts.Capacity)
	}
	return
}

// Go fmt, why you so shit?
const openConnFlags = 0 |
	sqlite.SQLITE_OPEN_READWRITE |
	sqlite.SQLITE_OPEN_CREATE |
	sqlite.SQLITE_OPEN_URI |
	sqlite.SQLITE_OPEN_NOMUTEX

func newConn(opts NewConnOpts) (conn, error) {
	return sqlite.OpenConn(newOpenUri(opts), openConnFlags)
}
