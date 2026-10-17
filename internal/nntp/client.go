package nntp

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// defaultMaxArticleBytes caps a single article read when no explicit limit is
// set. Articles arrive from Usenet, so their size is attacker-controlled.
const defaultMaxArticleBytes = 32 << 20 // 32 MiB

// ErrArticleTooLarge is returned when an article exceeds the configured cap.
var ErrArticleTooLarge = errors.New("nntp: article exceeds size limit")

// ErrNoSuchArticle is the server's 430: the article is gone (expired, or never there). The
// connection is still fine, unlike a failure to read a reply.
var ErrNoSuchArticle = errors.New("nntp: no such article")

// Client is a minimal NNTP client supporting the commands needed for Spotnet.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tls  bool
	addr string

	// maxArticleBytes bounds Body/BinaryBody/Head reads.
	maxArticleBytes int64
}

// SetMaxArticleBytes sets the per-article read ceiling. Zero restores the
// default.
func (c *Client) SetMaxArticleBytes(n int64) {
	if n <= 0 {
		n = defaultMaxArticleBytes
	}
	c.maxArticleBytes = n
}

// ArticleInfo is returned by OVER/XOVER for a single article.
type ArticleInfo struct {
	ArticleNum int64
	Subject    string
	From       string
	Date       time.Time
	MessageID  string
	References string
	Bytes      int64
	Lines      int64
}

// GroupInfo is returned by GROUP command.
type GroupInfo struct {
	Name  string
	Count int64
	First int64
	Last  int64
}

// Dial opens a (optionally TLS) connection and authenticates.
func Dial(host string, port int, useTLS bool, user, pass string) (*Client, error) {
	// JoinHostPort brackets an IPv6 address; "%s:%d" did not.
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var conn net.Conn
	var err error

	if useTLS {
		conn, err = tls.Dial("tcp", addr, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
	} else {
		conn, err = net.DialTimeout("tcp", addr, 30*time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c := &Client{
		conn:            conn,
		r:               bufio.NewReaderSize(conn, 1<<20), // 1MB read buffer
		w:               bufio.NewWriter(conn),
		tls:             useTLS,
		addr:            addr,
		maxArticleBytes: defaultMaxArticleBytes,
	}

	// Read greeting
	if _, err := c.readResponse(200, 201); err != nil {
		conn.Close()
		return nil, fmt.Errorf("greeting: %w", err)
	}

	// Authenticate if credentials provided
	if user != "" {
		if err := c.auth(user, pass); err != nil {
			conn.Close()
			return nil, err
		}
	}

	return c, nil
}

func (c *Client) Close() error {
	_, _ = c.sendCmd("QUIT")
	return c.conn.Close()
}

// SelectGroup selects a newsgroup and returns its info.
func (c *Client) SelectGroup(group string) (*GroupInfo, error) {
	resp, err := c.sendCmd("GROUP " + group)
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp, 211); err != nil {
		return nil, err
	}
	// 211 count first last name
	parts := strings.Fields(resp)
	if len(parts) < 5 {
		return nil, fmt.Errorf("unexpected GROUP response: %s", resp)
	}
	gi := &GroupInfo{Name: parts[4]}
	gi.Count, _ = strconv.ParseInt(parts[1], 10, 64)
	gi.First, _ = strconv.ParseInt(parts[2], 10, 64)
	gi.Last, _ = strconv.ParseInt(parts[3], 10, 64)
	return gi, nil
}

// Overview fetches article overviews for article numbers [from, to].
// Returns articles in order; large ranges should be chunked by caller.
func (c *Client) Overview(from, to int64) ([]ArticleInfo, error) {
	resp, err := c.sendCmd(fmt.Sprintf("OVER %d-%d", from, to))
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp, 224); err != nil {
		// Some servers use XOVER
		resp, err = c.sendCmd(fmt.Sprintf("XOVER %d-%d", from, to))
		if err != nil {
			return nil, err
		}
		if err := checkCode(resp, 224); err != nil {
			return nil, err
		}
	}

	var articles []ArticleInfo
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read overview line: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			break
		}

		parts := strings.SplitN(line, "\t", 9)
		if len(parts) < 8 {
			continue
		}

		ai := ArticleInfo{
			Subject:    parts[1],
			From:       parts[2],
			MessageID:  parts[4],
			References: parts[5],
		}
		ai.ArticleNum, _ = strconv.ParseInt(parts[0], 10, 64)
		ai.Bytes, _ = strconv.ParseInt(parts[6], 10, 64)
		ai.Lines, _ = strconv.ParseInt(parts[7], 10, 64)
		ai.Date, _ = parseNNTPDate(parts[3])
		articles = append(articles, ai)
	}

	return articles, nil
}

// Body fetches the raw body of an article by Message-ID.
//
// The id is normalized and validated here rather than at the call sites, so no
// caller can smuggle CRLF into the command stream regardless of where the id
// came from.
func (c *Client) Body(messageID string) ([]byte, error) {
	msgID, err := NormalizeMessageID(messageID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendCmd("BODY " + msgID)
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp, 222); err != nil {
		return nil, err
	}
	return c.readDotBody()
}

// BinaryBody fetches a body whose content is a binary-safe text encoding
// (e.g. Spotnet's specialZipStr). Lines are concatenated WITHOUT any separator
// so that the reassembled bytes match exactly what was posted.
func (c *Client) BinaryBody(messageID string) ([]byte, error) {
	msgID, err := NormalizeMessageID(messageID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendCmd("BODY " + msgID)
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp, 222); err != nil {
		return nil, err
	}
	return c.readDotBinaryBody()
}

// Head fetches the headers of an article by Message-ID.
// Returns a map of lowercase header name → concatenated value(s).
// Multi-line (folded) and repeated headers (e.g. X-XML) are concatenated.
func (c *Client) Head(messageID string) (map[string]string, error) {
	msgID, err := NormalizeMessageID(messageID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendCmd("HEAD " + msgID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(resp, "430") {
		return nil, ErrNoSuchArticle
	}
	if err := checkCode(resp, 221); err != nil {
		return nil, err
	}

	limit := c.maxArticleBytes
	if limit <= 0 {
		limit = defaultMaxArticleBytes
	}
	var read int64

	headers := make(map[string]string)
	var curKey, curVal string

	flush := func() {
		if curKey != "" {
			headers[curKey] += curVal
		}
	}

	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read head: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			break
		}
		// Spotnet carries the whole spot XML in repeated X-XML headers, so the
		// header block is itself attacker-sized and needs the same ceiling as
		// a body.
		read += int64(len(line)) + 1
		if read > limit {
			return nil, ErrArticleTooLarge
		}
		// Folded header continuation (starts with whitespace)
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			curVal += strings.TrimLeft(line, " \t")
			continue
		}
		flush()
		curKey = ""
		curVal = ""
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		curKey = strings.ToLower(strings.TrimSpace(line[:idx]))
		curVal = strings.TrimSpace(line[idx+1:])
	}
	flush()

	return headers, nil
}

// auth performs AUTHINFO USER/PASS authentication.
func (c *Client) auth(user, pass string) error {
	resp, err := c.sendCmd("AUTHINFO USER " + user)
	if err != nil {
		return err
	}
	// 281 = already authenticated, 381 = send password
	if strings.HasPrefix(resp, "281") {
		return nil
	}
	if err := checkCode(resp, 381); err != nil {
		return fmt.Errorf("authinfo user: %w", err)
	}
	resp, err = c.sendCmd("AUTHINFO PASS " + pass)
	if err != nil {
		return err
	}
	return checkCode(resp, 281)
}

func (c *Client) sendCmd(cmd string) (string, error) {
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(c.w, "%s\r\n", cmd); err != nil {
		return "", err
	}
	if err := c.w.Flush(); err != nil {
		return "", err
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	line, err := c.r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func (c *Client) readResponse(codes ...int) (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	for _, code := range codes {
		if strings.HasPrefix(line, strconv.Itoa(code)) {
			return line, nil
		}
	}
	return line, nil
}

func (c *Client) readDotBody() ([]byte, error) {
	return c.readDot(true)
}

// readDot reads a dot-terminated body, appending a newline per line when
// keepNewlines is set. The accumulated size is checked against
// maxArticleBytes on every line: an unbounded append here let a single
// oversized Usenet article exhaust process memory.
func (c *Client) readDot(keepNewlines bool) ([]byte, error) {
	limit := c.maxArticleBytes
	if limit <= 0 {
		limit = defaultMaxArticleBytes
	}

	var buf []byte
	for {
		line, err := c.r.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			break
		}
		// Unstuff leading dot
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}

		if int64(len(buf))+int64(len(trimmed))+1 > limit {
			// The connection still holds the remainder of the article, so the
			// caller must not reuse it.
			return nil, ErrArticleTooLarge
		}

		buf = append(buf, trimmed...)
		if keepNewlines {
			buf = append(buf, '\n')
		}
		if err == io.EOF {
			break
		}
	}
	return buf, nil
}

// readDotBinaryBody reads a dot-terminated NNTP body and concatenates lines
// WITHOUT any line separator. Used for Spotnet's specialZipStr-encoded articles
// where the encoder already escaped all special bytes (LF, CR, NUL) and the
// server may have wrapped lines for transport.
func (c *Client) readDotBinaryBody() ([]byte, error) {
	return c.readDot(false)
}

func checkCode(resp string, code int) error {
	prefix := strconv.Itoa(code)
	if strings.HasPrefix(resp, prefix) {
		return nil
	}
	return fmt.Errorf("expected %d, got: %s", code, resp)
}

// nntpDateFormats covers the shapes seen on real Usenet Date headers.
//
// Two-digit years matter: plenty of posting clients emit
// "Thu, 12 May 22 21:11:49 UTC". Without a "06" layout those fail to parse,
// and the old fallback silently substituted time.Now() — which dated thousands
// of historical spots to the moment they were indexed.
var nntpDateFormats = []string{
	// 4-digit year
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04 -0700",
	"2 Jan 2006 15:04 MST",
	// 2-digit year
	"Mon, 2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 06 15:04:05 MST",
	"Mon, 2 Jan 06 15:04 -0700",
	"Mon, 2 Jan 06 15:04 MST",
	"2 Jan 06 15:04:05 -0700",
	"2 Jan 06 15:04:05 MST",
	"2 Jan 06 15:04 -0700",
	"2 Jan 06 15:04 MST",
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
}

// parseNNTPDate tries common NNTP date formats.
//
// On failure it returns the zero time and an error — never time.Now(). A
// caller that ignores the error then gets an obviously-wrong zero value rather
// than a plausible-looking current timestamp that silently corrupts ordering.
func parseNNTPDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}

	// Some clients append a parenthesised zone name: "+0000 (UTC)".
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[:i])
	}

	for _, f := range nntpDateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date: %s", s)
}
