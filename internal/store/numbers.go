package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FormatNumber renders an inventory number from the format of
// inventory.number_format: {prefix} is replaced by the category prefix, {seq}
// by the running number and {seq:0N} by the number padded with zeros to
// width N. An empty prefix drops {prefix} together with the separators
// (-, _, /, . or space) that directly follow it, so "{prefix}-{seq:05}"
// gives "00042" for a category without prefix.
func FormatNumber(format, prefix string, seq int64) string {
	var b strings.Builder
	rest := format
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		tok := rest[i+1 : i+j]
		switch {
		case tok == "prefix":
			b.WriteString(prefix)
			if prefix == "" {
				rest = rest[i+j+1:]
				rest = strings.TrimLeft(rest, "-_/. ")
				continue
			}
		case tok == "seq":
			b.WriteString(strconv.FormatInt(seq, 10))
		case strings.HasPrefix(tok, "seq:0"):
			w, _ := strconv.Atoi(tok[5:])
			fmt.Fprintf(&b, "%0*d", w, seq)
		default:
			b.WriteString(rest[i : i+j+1])
		}
		rest = rest[i+j+1:]
	}
	return b.String()
}

// nextInventoryNo reserves the next free number inside the caller's
// transaction: it advances the sequence and skips numbers that are already
// taken (for example by manual or imported numbers). With {prefix} in the
// format every prefix has its own sequence (categories without prefix share
// one); without {prefix} all categories share a single sequence.
func nextInventoryNo(ctx context.Context, q queryer, format, prefix string) (string, error) {
	key := prefix
	if !strings.Contains(format, "{prefix}") {
		key = ""
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO number_sequences (prefix, next) VALUES (?, 1) ON CONFLICT (prefix) DO NOTHING`, key); err != nil {
		return "", err
	}
	var next int64
	if err := q.QueryRowContext(ctx, `SELECT next FROM number_sequences WHERE prefix = ?`, key).Scan(&next); err != nil {
		return "", err
	}
	for {
		no := FormatNumber(format, prefix, next)
		next++
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE inventory_no = ?`, no).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			if _, err := q.ExecContext(ctx, `UPDATE number_sequences SET next = ? WHERE prefix = ?`, next, key); err != nil {
				return "", err
			}
			return no, nil
		}
	}
}
