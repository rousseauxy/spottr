package nntp

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal NNTP client supporting the commands needed for Spotnet.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	w      *bufio.Writer
	tls    bool
	addr   string
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
	addr := fmt.Sprintf("%s:%d", host, port)
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
		conn: conn,
		r:    bufio.NewReaderSize(conn, 1<<20), // 1MB read buffer
		w:    bufio.NewWriter(conn),
		tls:  useTLS,
		addr: addr,
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
func (c *Client) Body(messageID string) ([]byte, error) {
	resp, err := c.sendCmd("BODY " + messageID)
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
	resp, err := c.sendCmd("BODY " + messageID)
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
	resp, err := c.sendCmd("HEAD " + messageID)
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp, 221); err != nil {
		return nil, err
	}

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
		buf = append(buf, []byte(trimmed+"\n")...)
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
		buf = append(buf, []byte(trimmed)...)
		if err == io.EOF {
			break
		}
	}
	return buf, nil
}

func checkCode(resp string, code int) error {
	prefix := strconv.Itoa(code)
	if strings.HasPrefix(resp, prefix) {
		return nil
	}
	return fmt.Errorf("expected %d, got: %s", code, resp)
}

// parseNNTPDate tries common NNTP date formats.
func parseNNTPDate(s string) (time.Time, error) {
	formats := []string{
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		"2 Jan 2006 15:04:05 MST",
		time.RFC1123Z,
		time.RFC1123,
	}
	s = strings.TrimSpace(s)
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Now(), fmt.Errorf("unrecognised date: %s", s)
}
