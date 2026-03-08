package chat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidInput    = errors.New("invalid input")
	ErrForbiddenChat   = errors.New("forbidden chat")
	ErrChatNotFound    = errors.New("chat not found")
	ErrUserNotFound    = errors.New("user not found")
	ErrMessageNotFound = errors.New("message not found")
)

type Service struct {
	db *pgxpool.Pool
}

type ChatMember struct {
	UserID      string  `json:"user_id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	Role        string  `json:"role"`
}

type LastMessage struct {
	ID       string    `json:"id"`
	SenderID string    `json:"sender_id"`
	Content  string    `json:"content"`
	SentAt   time.Time `json:"sent_at"`
}

type Chat struct {
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	Title         *string      `json:"title,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	LastMessageAt *time.Time   `json:"last_message_at,omitempty"`
	UnreadCount   int64        `json:"unread_count"`
	LastMessage   *LastMessage `json:"last_message,omitempty"`
	Members       []ChatMember `json:"members"`
}

type Message struct {
	ID          string    `json:"id"`
	ChatID      string    `json:"chat_id"`
	SenderID    string    `json:"sender_id"`
	ClientMsgID *string   `json:"client_msg_id,omitempty"`
	ContentType string    `json:"content_type"`
	Content     string    `json:"content"`
	SentAt      time.Time `json:"sent_at"`
}

type ReadReceipt struct {
	ChatID    string    `json:"chat_id"`
	MessageID string    `json:"message_id"`
	UserID    string    `json:"user_id"`
	ReadAt    time.Time `json:"read_at"`
}

type MessagePage struct {
	Messages   []Message  `json:"messages"`
	HasMore    bool       `json:"has_more"`
	NextBefore *time.Time `json:"next_before,omitempty"`
}

type UserSummary struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

type ListChatsOptions struct {
	Limit  int
	Offset int
}

type ListMessagesOptions struct {
	Limit  int
	Before *time.Time
}

type SendMessageInput struct {
	ChatID      string
	ClientMsgID string
	Content     string
}

type MarkReadInput struct {
	ChatID    string
	MessageID string
}

func NewService(db *pgxpool.Pool) (*Service, error) {
	if db == nil {
		return nil, errors.New("chat service requires postgres pool")
	}
	return &Service{db: db}, nil
}

func (s *Service) CreateDirectChat(ctx context.Context, requesterID, peerUserID string) (Chat, error) {
	requesterID = strings.TrimSpace(requesterID)
	peerUserID = strings.TrimSpace(peerUserID)

	if requesterID == "" || peerUserID == "" {
		return Chat{}, fmt.Errorf("%w: requester and peer user id are required", ErrInvalidInput)
	}
	if requesterID == peerUserID {
		return Chat{}, fmt.Errorf("%w: direct chat requires another user", ErrInvalidInput)
	}

	exists, err := s.userExists(ctx, peerUserID)
	if err != nil {
		return Chat{}, err
	}
	if !exists {
		return Chat{}, ErrUserNotFound
	}

	existingID, err := s.findExistingDirectChat(ctx, requesterID, peerUserID)
	if err != nil {
		return Chat{}, err
	}
	if existingID != "" {
		return s.GetChatForUser(ctx, requesterID, existingID)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Chat{}, fmt.Errorf("begin create direct chat tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var chatID string
	err = tx.QueryRow(ctx, `
		INSERT INTO chats (type, created_by)
		VALUES ('direct', $1)
		RETURNING id
	`, requesterID).Scan(&chatID)
	if err != nil {
		return Chat{}, fmt.Errorf("insert direct chat: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO chat_members (chat_id, user_id, role)
		VALUES
			($1, $2, 'member'),
			($1, $3, 'member')
	`, chatID, requesterID, peerUserID); err != nil {
		return Chat{}, fmt.Errorf("insert direct chat members: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Chat{}, fmt.Errorf("commit create direct chat tx: %w", err)
	}

	return s.GetChatForUser(ctx, requesterID, chatID)
}

func (s *Service) CreateGroupChat(ctx context.Context, requesterID, title string, memberUserIDs []string) (Chat, error) {
	requesterID = strings.TrimSpace(requesterID)
	title = strings.TrimSpace(title)
	if requesterID == "" {
		return Chat{}, fmt.Errorf("%w: requester id is required", ErrInvalidInput)
	}
	if title == "" {
		return Chat{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}

	memberIDs := uniqueIDs(memberUserIDs)
	memberIDs = append(memberIDs, requesterID)
	memberIDs = uniqueIDs(memberIDs)
	if len(memberIDs) < 2 {
		return Chat{}, fmt.Errorf("%w: group chat requires at least 2 members", ErrInvalidInput)
	}

	if err := s.ensureUsersExist(ctx, memberIDs); err != nil {
		return Chat{}, err
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Chat{}, fmt.Errorf("begin create group chat tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var chatID string
	err = tx.QueryRow(ctx, `
		INSERT INTO chats (type, title, created_by)
		VALUES ('group', $1, $2)
		RETURNING id
	`, title, requesterID).Scan(&chatID)
	if err != nil {
		return Chat{}, fmt.Errorf("insert group chat: %w", err)
	}

	for _, memberID := range memberIDs {
		role := "member"
		if memberID == requesterID {
			role = "owner"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat_members (chat_id, user_id, role)
			VALUES ($1, $2, $3)
		`, chatID, memberID, role); err != nil {
			return Chat{}, fmt.Errorf("insert group chat member: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Chat{}, fmt.Errorf("commit create group chat tx: %w", err)
	}

	return s.GetChatForUser(ctx, requesterID, chatID)
}

func (s *Service) ListChats(ctx context.Context, userID string, opts ListChatsOptions) ([]Chat, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("%w: user id is required", ErrInvalidInput)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.Query(ctx, `
		SELECT
			c.id,
			c.type,
			c.title,
			c.created_at,
			c.updated_at,
			c.last_message_at,
			lm.id,
			lm.sender_id,
			lm.content,
			lm.sent_at,
			COALESCE(uc.unread_count, 0) AS unread_count
		FROM chats c
		JOIN chat_members cm
			ON cm.chat_id = c.id
			AND cm.user_id = $1
		LEFT JOIN LATERAL (
			SELECT m.id, m.sender_id, m.content, m.sent_at
			FROM messages m
			WHERE m.chat_id = c.id
				AND m.deleted_at IS NULL
			ORDER BY m.sent_at DESC, m.id DESC
			LIMIT 1
		) lm ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*)::bigint AS unread_count
			FROM messages m
			WHERE m.chat_id = c.id
				AND m.deleted_at IS NULL
				AND m.sender_id <> $1
				AND (cm.last_read_at IS NULL OR m.sent_at > cm.last_read_at)
		) uc ON TRUE
		ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list chats query: %w", err)
	}
	defer rows.Close()

	chats := make([]Chat, 0)
	chatIDs := make([]string, 0)
	for rows.Next() {
		var c Chat
		var lmID, lmSenderID, lmContent *string
		var lmSentAt *time.Time
		if err := rows.Scan(
			&c.ID,
			&c.Type,
			&c.Title,
			&c.CreatedAt,
			&c.UpdatedAt,
			&c.LastMessageAt,
			&lmID,
			&lmSenderID,
			&lmContent,
			&lmSentAt,
			&c.UnreadCount,
		); err != nil {
			return nil, fmt.Errorf("list chats scan: %w", err)
		}

		if lmID != nil && lmSenderID != nil && lmContent != nil && lmSentAt != nil {
			c.LastMessage = &LastMessage{
				ID:       *lmID,
				SenderID: *lmSenderID,
				Content:  *lmContent,
				SentAt:   *lmSentAt,
			}
		}

		chats = append(chats, c)
		chatIDs = append(chatIDs, c.ID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chats rows: %w", err)
	}

	if len(chats) == 0 {
		return chats, nil
	}

	membersByChat, err := s.loadMembersByChat(ctx, chatIDs)
	if err != nil {
		return nil, err
	}

	for i := range chats {
		chats[i].Members = membersByChat[chats[i].ID]
	}

	return chats, nil
}

func (s *Service) GetChatForUser(ctx context.Context, userID, chatID string) (Chat, error) {
	items, err := s.ListChats(ctx, userID, ListChatsOptions{Limit: 200, Offset: 0})
	if err != nil {
		return Chat{}, err
	}

	for _, item := range items {
		if item.ID == chatID {
			return item, nil
		}
	}

	return Chat{}, ErrForbiddenChat
}

func (s *Service) ListMessages(ctx context.Context, userID, chatID string, opts ListMessagesOptions) (MessagePage, error) {
	userID = strings.TrimSpace(userID)
	chatID = strings.TrimSpace(chatID)
	if userID == "" || chatID == "" {
		return MessagePage{}, fmt.Errorf("%w: user id and chat id are required", ErrInvalidInput)
	}

	isMember, err := s.isMember(ctx, userID, chatID)
	if err != nil {
		return MessagePage{}, err
	}
	if !isMember {
		return MessagePage{}, ErrForbiddenChat
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	fetchLimit := limit + 1

	messages := make([]Message, 0, fetchLimit)
	if opts.Before != nil {
		rows, err := s.db.Query(ctx, `
			SELECT id, chat_id, sender_id, client_msg_id, content_type, content, sent_at
			FROM messages
			WHERE chat_id = $1
				AND deleted_at IS NULL
				AND sent_at < $2
			ORDER BY sent_at DESC, id DESC
			LIMIT $3
		`, chatID, opts.Before.UTC(), fetchLimit)
		if err != nil {
			return MessagePage{}, fmt.Errorf("list messages query: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.ClientMsgID, &m.ContentType, &m.Content, &m.SentAt); err != nil {
				return MessagePage{}, fmt.Errorf("list messages scan: %w", err)
			}
			messages = append(messages, m)
		}
		if err := rows.Err(); err != nil {
			return MessagePage{}, fmt.Errorf("list messages rows: %w", err)
		}
	} else {
		rows, err := s.db.Query(ctx, `
			SELECT id, chat_id, sender_id, client_msg_id, content_type, content, sent_at
			FROM messages
			WHERE chat_id = $1
				AND deleted_at IS NULL
			ORDER BY sent_at DESC, id DESC
			LIMIT $2
		`, chatID, fetchLimit)
		if err != nil {
			return MessagePage{}, fmt.Errorf("list messages query: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.ClientMsgID, &m.ContentType, &m.Content, &m.SentAt); err != nil {
				return MessagePage{}, fmt.Errorf("list messages scan: %w", err)
			}
			messages = append(messages, m)
		}
		if err := rows.Err(); err != nil {
			return MessagePage{}, fmt.Errorf("list messages rows: %w", err)
		}
	}

	page := MessagePage{Messages: messages, HasMore: false}
	if len(messages) > limit {
		page.HasMore = true
		page.Messages = messages[:limit]
	}
	if len(page.Messages) > 0 {
		next := page.Messages[len(page.Messages)-1].SentAt
		page.NextBefore = &next
	}

	return page, nil
}

func (s *Service) SendMessage(ctx context.Context, senderUserID string, input SendMessageInput) (Message, []string, error) {
	senderUserID = strings.TrimSpace(senderUserID)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.ClientMsgID = strings.TrimSpace(input.ClientMsgID)
	input.Content = strings.TrimSpace(input.Content)

	if senderUserID == "" || input.ChatID == "" {
		return Message{}, nil, fmt.Errorf("%w: sender and chat id are required", ErrInvalidInput)
	}
	if input.Content == "" {
		return Message{}, nil, fmt.Errorf("%w: content is required", ErrInvalidInput)
	}
	if len(input.Content) > 4000 {
		return Message{}, nil, fmt.Errorf("%w: content is too long", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Message{}, nil, fmt.Errorf("begin send message tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	isMember, err := s.isMemberTx(ctx, tx, senderUserID, input.ChatID)
	if err != nil {
		return Message{}, nil, err
	}
	if !isMember {
		return Message{}, nil, ErrForbiddenChat
	}

	msg, err := insertMessageTx(ctx, tx, senderUserID, input)
	if err != nil {
		return Message{}, nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE chats
		SET last_message_at = $2, updated_at = NOW()
		WHERE id = $1
	`, input.ChatID, msg.SentAt); err != nil {
		return Message{}, nil, fmt.Errorf("update chat last message: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Message{}, nil, fmt.Errorf("commit send message tx: %w", err)
	}

	recipients, err := s.chatRecipients(ctx, input.ChatID)
	if err != nil {
		return Message{}, nil, err
	}

	return msg, recipients, nil
}

func insertMessageTx(ctx context.Context, tx pgx.Tx, senderUserID string, input SendMessageInput) (Message, error) {
	var msg Message
	if input.ClientMsgID == "" {
		err := tx.QueryRow(ctx, `
			INSERT INTO messages (chat_id, sender_id, content_type, content)
			VALUES ($1, $2, 'text', $3)
			RETURNING id, chat_id, sender_id, client_msg_id, content_type, content, sent_at
		`, input.ChatID, senderUserID, input.Content).Scan(
			&msg.ID,
			&msg.ChatID,
			&msg.SenderID,
			&msg.ClientMsgID,
			&msg.ContentType,
			&msg.Content,
			&msg.SentAt,
		)
		if err != nil {
			return Message{}, fmt.Errorf("insert message: %w", err)
		}
		return msg, nil
	}

	err := tx.QueryRow(ctx, `
		INSERT INTO messages (chat_id, sender_id, client_msg_id, content_type, content)
		VALUES ($1, $2, $3, 'text', $4)
		ON CONFLICT (chat_id, sender_id, client_msg_id) WHERE client_msg_id IS NOT NULL
		DO NOTHING
		RETURNING id, chat_id, sender_id, client_msg_id, content_type, content, sent_at
	`, input.ChatID, senderUserID, input.ClientMsgID, input.Content).Scan(
		&msg.ID,
		&msg.ChatID,
		&msg.SenderID,
		&msg.ClientMsgID,
		&msg.ContentType,
		&msg.Content,
		&msg.SentAt,
	)
	if err == nil {
		return msg, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Message{}, fmt.Errorf("insert message with idempotency: %w", err)
	}

	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, sender_id, client_msg_id, content_type, content, sent_at
		FROM messages
		WHERE chat_id = $1 AND sender_id = $2 AND client_msg_id = $3
	`, input.ChatID, senderUserID, input.ClientMsgID).Scan(
		&msg.ID,
		&msg.ChatID,
		&msg.SenderID,
		&msg.ClientMsgID,
		&msg.ContentType,
		&msg.Content,
		&msg.SentAt,
	)
	if err != nil {
		return Message{}, fmt.Errorf("load existing idempotent message: %w", err)
	}

	return msg, nil
}

func (s *Service) MarkRead(ctx context.Context, userID string, input MarkReadInput) (ReadReceipt, []string, error) {
	userID = strings.TrimSpace(userID)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	if userID == "" || input.ChatID == "" || input.MessageID == "" {
		return ReadReceipt{}, nil, fmt.Errorf("%w: user id, chat id and message id are required", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReadReceipt{}, nil, fmt.Errorf("begin mark read tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	isMember, err := s.isMemberTx(ctx, tx, userID, input.ChatID)
	if err != nil {
		return ReadReceipt{}, nil, err
	}
	if !isMember {
		return ReadReceipt{}, nil, ErrForbiddenChat
	}

	var messageSentAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT sent_at
		FROM messages
		WHERE id = $1 AND chat_id = $2 AND deleted_at IS NULL
	`, input.MessageID, input.ChatID).Scan(&messageSentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReadReceipt{}, nil, ErrMessageNotFound
	}
	if err != nil {
		return ReadReceipt{}, nil, fmt.Errorf("load message for read receipt: %w", err)
	}

	readAt := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO message_reads (message_id, user_id, read_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (message_id, user_id)
		DO UPDATE SET read_at = EXCLUDED.read_at
	`, input.MessageID, userID, readAt); err != nil {
		return ReadReceipt{}, nil, fmt.Errorf("insert message read receipt: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE chat_members
		SET last_read_message_id = $3,
			last_read_at = GREATEST(COALESCE(last_read_at, $4), $4)
		WHERE chat_id = $1 AND user_id = $2
	`, input.ChatID, userID, input.MessageID, messageSentAt); err != nil {
		return ReadReceipt{}, nil, fmt.Errorf("update chat member read state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ReadReceipt{}, nil, fmt.Errorf("commit mark read tx: %w", err)
	}

	recipients, err := s.chatRecipients(ctx, input.ChatID)
	if err != nil {
		return ReadReceipt{}, nil, err
	}

	return ReadReceipt{
		ChatID:    input.ChatID,
		MessageID: input.MessageID,
		UserID:    userID,
		ReadAt:    readAt,
	}, recipients, nil
}

func (s *Service) ChatRecipientsForMember(ctx context.Context, userID, chatID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	chatID = strings.TrimSpace(chatID)
	if userID == "" || chatID == "" {
		return nil, fmt.Errorf("%w: user id and chat id are required", ErrInvalidInput)
	}

	isMember, err := s.isMember(ctx, userID, chatID)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return nil, ErrForbiddenChat
	}

	return s.chatRecipients(ctx, chatID)
}

func (s *Service) SearchUsers(ctx context.Context, requesterID, query string, limit int) ([]UserSummary, error) {
	requesterID = strings.TrimSpace(requesterID)
	query = strings.TrimSpace(query)
	if requesterID == "" {
		return nil, fmt.Errorf("%w: requester id is required", ErrInvalidInput)
	}
	if query == "" {
		return []UserSummary{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	like := "%" + query + "%"
	rows, err := s.db.Query(ctx, `
		SELECT id, email, username, display_name, avatar_url
		FROM users
		WHERE is_active = TRUE
			AND id <> $1
			AND (
				username ILIKE $2
				OR email ILIKE $2
				OR COALESCE(display_name, '') ILIKE $2
			)
		ORDER BY username ASC
		LIMIT $3
	`, requesterID, like, limit)
	if err != nil {
		return nil, fmt.Errorf("search users query: %w", err)
	}
	defer rows.Close()

	users := make([]UserSummary, 0)
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Email, &u.Username, &u.DisplayName, &u.AvatarURL); err != nil {
			return nil, fmt.Errorf("search users scan: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search users rows: %w", err)
	}

	return users, nil
}

func (s *Service) findExistingDirectChat(ctx context.Context, requesterID, peerUserID string) (string, error) {
	var chatID string
	err := s.db.QueryRow(ctx, `
		SELECT c.id
		FROM chats c
		JOIN chat_members cm1
			ON cm1.chat_id = c.id
			AND cm1.user_id = $1
		JOIN chat_members cm2
			ON cm2.chat_id = c.id
			AND cm2.user_id = $2
		WHERE c.type = 'direct'
			AND (SELECT COUNT(*) FROM chat_members cm WHERE cm.chat_id = c.id) = 2
		LIMIT 1
	`, requesterID, peerUserID).Scan(&chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find existing direct chat: %w", err)
	}

	return chatID, nil
}

func (s *Service) ensureUsersExist(ctx context.Context, userIDs []string) error {
	if len(userIDs) == 0 {
		return fmt.Errorf("%w: users are required", ErrInvalidInput)
	}

	var count int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM users
		WHERE id = ANY($1::uuid[])
			AND is_active = TRUE
	`, userIDs).Scan(&count)
	if err != nil {
		return fmt.Errorf("ensure users exist: %w", err)
	}

	if count != len(userIDs) {
		return ErrUserNotFound
	}
	return nil
}

func (s *Service) userExists(ctx context.Context, userID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM users
			WHERE id = $1
				AND is_active = TRUE
		)
	`, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check user exists: %w", err)
	}
	return exists, nil
}

func (s *Service) isMember(ctx context.Context, userID, chatID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM chat_members
			WHERE chat_id = $1
				AND user_id = $2
		)
	`, chatID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check chat membership: %w", err)
	}
	return exists, nil
}

func (s *Service) isMemberTx(ctx context.Context, tx pgx.Tx, userID, chatID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM chat_members
			WHERE chat_id = $1
				AND user_id = $2
		)
	`, chatID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check chat membership in tx: %w", err)
	}
	return exists, nil
}

func (s *Service) chatRecipients(ctx context.Context, chatID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT user_id
		FROM chat_members
		WHERE chat_id = $1
	`, chatID)
	if err != nil {
		return nil, fmt.Errorf("load chat recipients: %w", err)
	}
	defer rows.Close()

	recipients := make([]string, 0)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan chat recipient: %w", err)
		}
		recipients = append(recipients, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat recipients rows: %w", err)
	}

	return recipients, nil
}

func (s *Service) loadMembersByChat(ctx context.Context, chatIDs []string) (map[string][]ChatMember, error) {
	membersByChat := make(map[string][]ChatMember, len(chatIDs))
	if len(chatIDs) == 0 {
		return membersByChat, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT cm.chat_id, u.id, u.username, u.display_name, u.avatar_url, cm.role
		FROM chat_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.chat_id = ANY($1::uuid[])
		ORDER BY cm.joined_at ASC
	`, chatIDs)
	if err != nil {
		return nil, fmt.Errorf("load chat members query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var chatID string
		var member ChatMember
		if err := rows.Scan(&chatID, &member.UserID, &member.Username, &member.DisplayName, &member.AvatarURL, &member.Role); err != nil {
			return nil, fmt.Errorf("load chat members scan: %w", err)
		}
		membersByChat[chatID] = append(membersByChat[chatID], member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load chat members rows: %w", err)
	}

	return membersByChat, nil
}

func uniqueIDs(ids []string) []string {
	set := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := set[id]; exists {
			continue
		}
		set[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
