package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Model structs

type Agent struct {
	ID        string    `json:"id"`
	APIKey    string    `json:"api_key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Commit struct {
	Hash         string    `json:"hash"`
	ParentHash   string    `json:"parent_hash"`
	ParentHashes []string  `json:"parent_hashes,omitempty"`
	AgentID      string    `json:"agent_id"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
}

type Channel struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type Post struct {
	ID        int       `json:"id"`
	ChannelID int       `json:"channel_id"`
	AgentID   string    `json:"agent_id"`
	ParentID  *int      `json:"parent_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// DB wraps the SQLite connection.
type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite pragmas for performance and correctness
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := sqldb.Exec(pragma); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("set pragma %q: %w", pragma, err)
		}
	}
	return &DB{db: sqldb}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) Migrate() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			api_key TEXT UNIQUE NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS commits (
			hash TEXT PRIMARY KEY,
			parent_hash TEXT,
			agent_id TEXT REFERENCES agents(id),
			message TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS commit_parents (
			commit_hash TEXT NOT NULL REFERENCES commits(hash) ON DELETE CASCADE,
			parent_hash TEXT NOT NULL,
			parent_index INTEGER NOT NULL,
			PRIMARY KEY (commit_hash, parent_index),
			UNIQUE (commit_hash, parent_hash)
		);

		CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			description TEXT DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER NOT NULL REFERENCES channels(id),
			agent_id TEXT NOT NULL REFERENCES agents(id),
			parent_id INTEGER REFERENCES posts(id),
			content TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS rate_limits (
			agent_id TEXT NOT NULL,
			action TEXT NOT NULL,
			window_start TIMESTAMP NOT NULL,
			count INTEGER DEFAULT 1,
			PRIMARY KEY (agent_id, action, window_start)
		);

		CREATE INDEX IF NOT EXISTS idx_commits_parent ON commits(parent_hash);
		CREATE INDEX IF NOT EXISTS idx_commits_agent ON commits(agent_id);
		CREATE INDEX IF NOT EXISTS idx_commit_parents_parent ON commit_parents(parent_hash);
		CREATE INDEX IF NOT EXISTS idx_commit_parents_commit ON commit_parents(commit_hash, parent_index);
		CREATE INDEX IF NOT EXISTS idx_posts_channel ON posts(channel_id);
		CREATE INDEX IF NOT EXISTS idx_posts_parent ON posts(parent_id);

		INSERT OR IGNORE INTO commit_parents (commit_hash, parent_hash, parent_index)
		SELECT hash, parent_hash, 0
		FROM commits
		WHERE parent_hash IS NOT NULL AND parent_hash <> '';
	`)
	return err
}

// --- Agents ---

func (d *DB) CreateAgent(id, apiKey string) error {
	_, err := d.db.Exec("INSERT INTO agents (id, api_key) VALUES (?, ?)", id, apiKey)
	return err
}

func (d *DB) GetAgentByAPIKey(apiKey string) (*Agent, error) {
	var a Agent
	err := d.db.QueryRow("SELECT id, api_key, created_at FROM agents WHERE api_key = ?", apiKey).
		Scan(&a.ID, &a.APIKey, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

func (d *DB) GetAgentByID(id string) (*Agent, error) {
	var a Agent
	err := d.db.QueryRow("SELECT id, api_key, created_at FROM agents WHERE id = ?", id).
		Scan(&a.ID, &a.APIKey, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

// --- Commits ---

func (d *DB) InsertCommit(hash string, parentHashes []string, agentID, message string) error {
	primaryParent := firstParent(parentHashes)
	var agentValue any
	if agentID != "" {
		agentValue = agentID
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO commits (hash, parent_hash, agent_id, message) VALUES (?, ?, ?, ?)",
		hash, primaryParent, agentValue, message,
	); err != nil {
		return err
	}
	if err := insertCommitParents(tx, hash, parentHashes); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SyncCommitParents(hash string, parentHashes []string) error {
	primaryParent := firstParent(parentHashes)
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE commits SET parent_hash = ? WHERE hash = ?", primaryParent, hash); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM commit_parents WHERE commit_hash = ?", hash); err != nil {
		return err
	}
	if err := insertCommitParents(tx, hash, parentHashes); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) GetCommit(hash string) (*Commit, error) {
	var c Commit
	var parentHash sql.NullString
	var agentID sql.NullString
	err := d.db.QueryRow(
		"SELECT hash, parent_hash, agent_id, message, created_at FROM commits WHERE hash = ?", hash,
	).Scan(&c.Hash, &parentHash, &agentID, &c.Message, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if parentHash.Valid {
		c.ParentHash = parentHash.String
	}
	if agentID.Valid {
		c.AgentID = agentID.String
	}
	if err := d.populateParentHashes(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) ListCommits(agentID string, limit, offset int) ([]Commit, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if agentID != "" {
		rows, err = d.db.Query(
			"SELECT hash, parent_hash, agent_id, message, created_at FROM commits WHERE agent_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?",
			agentID, limit, offset,
		)
	} else {
		rows, err = d.db.Query(
			"SELECT hash, parent_hash, agent_id, message, created_at FROM commits ORDER BY created_at DESC LIMIT ? OFFSET ?",
			limit, offset,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commits, err := scanCommits(rows)
	if err != nil {
		return nil, err
	}
	if err := d.populateParentHashesFor(commits); err != nil {
		return nil, err
	}
	return commits, nil
}

func (d *DB) GetChildren(hash string) ([]Commit, error) {
	rows, err := d.db.Query(
		`SELECT DISTINCT c.hash, c.parent_hash, c.agent_id, c.message, c.created_at
		 FROM commits c
		 JOIN commit_parents cp ON cp.commit_hash = c.hash
		 WHERE cp.parent_hash = ?
		 ORDER BY c.created_at DESC`,
		hash,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commits, err := scanCommits(rows)
	if err != nil {
		return nil, err
	}
	if err := d.populateParentHashesFor(commits); err != nil {
		return nil, err
	}
	return commits, nil
}

func (d *DB) GetLineage(hash string) ([]Commit, error) {
	var lineage []Commit
	queue := []string{hash}
	seen := map[string]bool{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == "" || seen[current] {
			continue
		}
		seen[current] = true

		c, err := d.GetCommit(current)
		if err != nil {
			return lineage, err
		}
		if c == nil {
			continue
		}
		lineage = append(lineage, *c)
		for _, parentHash := range c.ParentHashes {
			if parentHash != "" && !seen[parentHash] {
				queue = append(queue, parentHash)
			}
		}
	}
	return lineage, nil
}

func (d *DB) GetLeaves() ([]Commit, error) {
	rows, err := d.db.Query(`
		SELECT c.hash, c.parent_hash, c.agent_id, c.message, c.created_at
		FROM commits c
			LEFT JOIN commit_parents child ON child.parent_hash = c.hash
			WHERE child.commit_hash IS NULL
		ORDER BY c.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commits, err := scanCommits(rows)
	if err != nil {
		return nil, err
	}
	if err := d.populateParentHashesFor(commits); err != nil {
		return nil, err
	}
	return commits, nil
}

func insertCommitParents(tx *sql.Tx, hash string, parentHashes []string) error {
	for i, parentHash := range parentHashes {
		if parentHash == "" {
			continue
		}
		if _, err := tx.Exec(
			"INSERT INTO commit_parents (commit_hash, parent_hash, parent_index) VALUES (?, ?, ?)",
			hash, parentHash, i,
		); err != nil {
			return err
		}
	}
	return nil
}

func firstParent(parentHashes []string) string {
	for _, parentHash := range parentHashes {
		if parentHash != "" {
			return parentHash
		}
	}
	return ""
}

func (d *DB) populateParentHashes(c *Commit) error {
	parents, err := d.getParentHashes(c.Hash)
	if err != nil {
		return err
	}
	if len(parents) == 0 && c.ParentHash != "" {
		parents = []string{c.ParentHash}
	}
	c.ParentHashes = parents
	if c.ParentHash == "" {
		c.ParentHash = firstParent(parents)
	}
	return nil
}

func (d *DB) populateParentHashesFor(commits []Commit) error {
	if len(commits) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(commits))
	for _, c := range commits {
		hashes = append(hashes, c.Hash)
	}
	parentMap, err := d.getParentHashesMap(hashes)
	if err != nil {
		return err
	}
	for i := range commits {
		parents := parentMap[commits[i].Hash]
		if len(parents) == 0 && commits[i].ParentHash != "" {
			parents = []string{commits[i].ParentHash}
		}
		commits[i].ParentHashes = parents
		if commits[i].ParentHash == "" {
			commits[i].ParentHash = firstParent(parents)
		}
	}
	return nil
}

func (d *DB) getParentHashes(hash string) ([]string, error) {
	parentMap, err := d.getParentHashesMap([]string{hash})
	if err != nil {
		return nil, err
	}
	return parentMap[hash], nil
}

func (d *DB) getParentHashesMap(hashes []string) (map[string][]string, error) {
	parentMap := make(map[string][]string, len(hashes))
	if len(hashes) == 0 {
		return parentMap, nil
	}
	placeholders := make([]string, len(hashes))
	args := make([]any, len(hashes))
	for i, hash := range hashes {
		placeholders[i] = "?"
		args[i] = hash
	}
	rows, err := d.db.Query(
		fmt.Sprintf(
			"SELECT commit_hash, parent_hash FROM commit_parents WHERE commit_hash IN (%s) ORDER BY commit_hash, parent_index",
			strings.Join(placeholders, ","),
		),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var commitHash, parentHash string
		if err := rows.Scan(&commitHash, &parentHash); err != nil {
			return nil, err
		}
		parentMap[commitHash] = append(parentMap[commitHash], parentHash)
	}
	return parentMap, rows.Err()
}

func scanCommits(rows *sql.Rows) ([]Commit, error) {
	var commits []Commit
	for rows.Next() {
		var c Commit
		var parentHash sql.NullString
		var agentID sql.NullString
		if err := rows.Scan(&c.Hash, &parentHash, &agentID, &c.Message, &c.CreatedAt); err != nil {
			return nil, err
		}
		if parentHash.Valid {
			c.ParentHash = parentHash.String
		}
		if agentID.Valid {
			c.AgentID = agentID.String
		}
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

// --- Channels ---

func (d *DB) CreateChannel(name, description string) error {
	_, err := d.db.Exec("INSERT INTO channels (name, description) VALUES (?, ?)", name, description)
	return err
}

func (d *DB) ListChannels() ([]Channel, error) {
	rows, err := d.db.Query("SELECT id, name, description, created_at FROM channels ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []Channel
	for rows.Next() {
		var ch Channel
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.CreatedAt); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (d *DB) GetChannelByName(name string) (*Channel, error) {
	var ch Channel
	err := d.db.QueryRow("SELECT id, name, description, created_at FROM channels WHERE name = ?", name).
		Scan(&ch.ID, &ch.Name, &ch.Description, &ch.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ch, err
}

// --- Posts ---

func (d *DB) CreatePost(channelID int, agentID string, parentID *int, content string) (*Post, error) {
	res, err := d.db.Exec(
		"INSERT INTO posts (channel_id, agent_id, parent_id, content) VALUES (?, ?, ?, ?)",
		channelID, agentID, parentID, content,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetPost(int(id))
}

func (d *DB) ListPosts(channelID, limit, offset int) ([]Post, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(
		"SELECT id, channel_id, agent_id, parent_id, content, created_at FROM posts WHERE channel_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?",
		channelID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPosts(rows)
}

func (d *DB) GetPost(id int) (*Post, error) {
	var p Post
	var parentID sql.NullInt64
	err := d.db.QueryRow(
		"SELECT id, channel_id, agent_id, parent_id, content, created_at FROM posts WHERE id = ?", id,
	).Scan(&p.ID, &p.ChannelID, &p.AgentID, &parentID, &p.Content, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if parentID.Valid {
		v := int(parentID.Int64)
		p.ParentID = &v
	}
	return &p, err
}

func (d *DB) GetReplies(postID int) ([]Post, error) {
	rows, err := d.db.Query(
		"SELECT id, channel_id, agent_id, parent_id, content, created_at FROM posts WHERE parent_id = ? ORDER BY created_at ASC",
		postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPosts(rows)
}

func scanPosts(rows *sql.Rows) ([]Post, error) {
	var posts []Post
	for rows.Next() {
		var p Post
		var parentID sql.NullInt64
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.AgentID, &parentID, &p.Content, &p.CreatedAt); err != nil {
			return nil, err
		}
		if parentID.Valid {
			v := int(parentID.Int64)
			p.ParentID = &v
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// --- Dashboard queries ---

type Stats struct {
	AgentCount  int
	CommitCount int
	PostCount   int
}

func (d *DB) GetStats() (*Stats, error) {
	var s Stats
	d.db.QueryRow("SELECT COUNT(*) FROM agents").Scan(&s.AgentCount)
	d.db.QueryRow("SELECT COUNT(*) FROM commits").Scan(&s.CommitCount)
	d.db.QueryRow("SELECT COUNT(*) FROM posts").Scan(&s.PostCount)
	return &s, nil
}

func (d *DB) ListAgents() ([]Agent, error) {
	rows, err := d.db.Query("SELECT id, '', created_at FROM agents ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.APIKey, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.APIKey = "" // never expose
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// RecentPosts returns recent posts across all channels with channel name joined in.
type PostWithChannel struct {
	Post
	ChannelName string
}

func (d *DB) RecentPosts(limit int) ([]PostWithChannel, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(`
		SELECT p.id, p.channel_id, p.agent_id, p.parent_id, p.content, p.created_at, c.name
		FROM posts p JOIN channels c ON p.channel_id = c.id
		ORDER BY p.created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []PostWithChannel
	for rows.Next() {
		var p PostWithChannel
		var parentID sql.NullInt64
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.AgentID, &parentID, &p.Content, &p.CreatedAt, &p.ChannelName); err != nil {
			return nil, err
		}
		if parentID.Valid {
			v := int(parentID.Int64)
			p.ParentID = &v
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// --- Rate Limiting ---

// CheckRateLimit returns true if the agent is within the allowed rate.
func (d *DB) CheckRateLimit(agentID, action string, maxPerHour int) (bool, error) {
	var count int
	err := d.db.QueryRow(
		"SELECT COALESCE(SUM(count), 0) FROM rate_limits WHERE agent_id = ? AND action = ? AND window_start > datetime('now', '-1 hour')",
		agentID, action,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count < maxPerHour, nil
}

func (d *DB) IncrementRateLimit(agentID, action string) error {
	_, err := d.db.Exec(`
		INSERT INTO rate_limits (agent_id, action, window_start, count)
		VALUES (?, ?, strftime('%Y-%m-%d %H:%M:00', 'now'), 1)
		ON CONFLICT(agent_id, action, window_start) DO UPDATE SET count = count + 1
	`, agentID, action)
	return err
}

func (d *DB) CleanupRateLimits() error {
	_, err := d.db.Exec("DELETE FROM rate_limits WHERE window_start < datetime('now', '-2 hours')")
	return err
}
