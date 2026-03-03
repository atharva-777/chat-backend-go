package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const accessTokenType = "access"

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid token")
	ErrTokenExpired       = errors.New("token expired")
	ErrEmailExists        = errors.New("email already exists")
	ErrUsernameExists     = errors.New("username already exists")
	ErrUserNotFound       = errors.New("user not found")
)

// Service manages authentication and authorization concerns.
type Service struct {
	db         *pgxpool.Pool
	jwtSecret  []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

type ClientMeta struct {
	UserAgent string
	IPAddress string
}

type RegisterInput struct {
	Email       string
	Username    string
	Password    string
	DisplayName string
}

type LoginInput struct {
	Email    string
	Password string
}

type User struct {
	ID          string
	Email       string
	Username    string
	DisplayName *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AuthResult struct {
	User                 User
	AccessToken          string
	RefreshToken         string
	AccessTokenExpiresAt time.Time
	RefreshTokenExpires  time.Time
}

type AccessClaims struct {
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

type refreshTokenRow struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewService(db *pgxpool.Pool, jwtSecret string, accessTTL, refreshTTL time.Duration) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth service requires postgres pool")
	}
	if strings.TrimSpace(jwtSecret) == "" {
		return nil, errors.New("JWT_SECRET is required")
	}
	if accessTTL <= 0 {
		return nil, errors.New("access token ttl must be > 0")
	}
	if refreshTTL <= 0 {
		return nil, errors.New("refresh token ttl must be > 0")
	}

	return &Service{
		db:         db,
		jwtSecret:  []byte(jwtSecret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}, nil
}

func (s *Service) Register(ctx context.Context, input RegisterInput, meta ClientMeta) (AuthResult, error) {
	normalizedEmail := normalizeEmail(input.Email)
	normalizedUsername := strings.TrimSpace(input.Username)
	normalizedDisplayName := strings.TrimSpace(input.DisplayName)

	if normalizedEmail == "" {
		return AuthResult{}, fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	if normalizedUsername == "" {
		return AuthResult{}, fmt.Errorf("%w: username is required", ErrInvalidInput)
	}
	if len(input.Password) < 8 {
		return AuthResult{}, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}

	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return AuthResult{}, fmt.Errorf("hash password: %w", err)
	}

	var displayNameValue any
	if normalizedDisplayName != "" {
		displayNameValue = normalizedDisplayName
	}

	var user User
	err = s.db.QueryRow(ctx, `
		INSERT INTO users (email, username, password_hash, display_name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, email, username, display_name, created_at, updated_at
	`, normalizedEmail, normalizedUsername, passwordHash, displayNameValue).Scan(
		&user.ID,
		&user.Email,
		&user.Username,
		&user.DisplayName,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		return AuthResult{}, mapUniqueConstraintError(err)
	}

	accessToken, accessExpiresAt, err := s.issueAccessToken(user.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue access token: %w", err)
	}

	refreshToken, _, refreshExpiresAt, err := s.insertRefreshToken(ctx, s.db, user.ID, meta)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue refresh token: %w", err)
	}

	return AuthResult{
		User:                 user,
		AccessToken:          accessToken,
		RefreshToken:         refreshToken,
		AccessTokenExpiresAt: accessExpiresAt,
		RefreshTokenExpires:  refreshExpiresAt,
	}, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput, meta ClientMeta) (AuthResult, error) {
	normalizedEmail := normalizeEmail(input.Email)
	if normalizedEmail == "" {
		return AuthResult{}, fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	if strings.TrimSpace(input.Password) == "" {
		return AuthResult{}, fmt.Errorf("%w: password is required", ErrInvalidInput)
	}

	var user User
	var passwordHash string
	err := s.db.QueryRow(ctx, `
		SELECT id, email, username, password_hash, display_name, created_at, updated_at
		FROM users
		WHERE email = $1 AND is_active = TRUE
	`, normalizedEmail).Scan(
		&user.ID,
		&user.Email,
		&user.Username,
		&passwordHash,
		&user.DisplayName,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return AuthResult{}, fmt.Errorf("find user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)); err != nil {
		return AuthResult{}, ErrInvalidCredentials
	}

	accessToken, accessExpiresAt, err := s.issueAccessToken(user.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue access token: %w", err)
	}

	refreshToken, _, refreshExpiresAt, err := s.insertRefreshToken(ctx, s.db, user.ID, meta)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue refresh token: %w", err)
	}

	return AuthResult{
		User:                 user,
		AccessToken:          accessToken,
		RefreshToken:         refreshToken,
		AccessTokenExpiresAt: accessExpiresAt,
		RefreshTokenExpires:  refreshExpiresAt,
	}, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string, meta ClientMeta) (AuthResult, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return AuthResult{}, fmt.Errorf("%w: refresh_token is required", ErrInvalidInput)
	}

	tokenHash := hashToken(refreshToken)
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthResult{}, fmt.Errorf("begin refresh tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var stored refreshTokenRow
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, expires_at, revoked_at
		FROM refresh_tokens
		WHERE token_hash = $1
		FOR UPDATE
	`, tokenHash).Scan(&stored.ID, &stored.UserID, &stored.ExpiresAt, &stored.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthResult{}, ErrInvalidToken
	}
	if err != nil {
		return AuthResult{}, fmt.Errorf("load refresh token: %w", err)
	}

	if stored.RevokedAt != nil {
		return AuthResult{}, ErrInvalidToken
	}
	if now.After(stored.ExpiresAt) {
		_, _ = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $1 WHERE id = $2 AND revoked_at IS NULL`, now, stored.ID)
		return AuthResult{}, ErrTokenExpired
	}

	accessToken, accessExpiresAt, err := s.issueAccessToken(stored.UserID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue access token: %w", err)
	}

	newRefreshToken, newTokenID, refreshExpiresAt, err := s.insertRefreshToken(ctx, tx, stored.UserID, meta)
	if err != nil {
		return AuthResult{}, fmt.Errorf("issue refresh token: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE refresh_tokens
		SET revoked_at = $1, replaced_by_token_id = $2
		WHERE id = $3
	`, now, newTokenID, stored.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("rotate refresh token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AuthResult{}, fmt.Errorf("commit refresh tx: %w", err)
	}

	user, err := s.GetUserByID(ctx, stored.UserID)
	if err != nil {
		return AuthResult{}, err
	}

	return AuthResult{
		User:                 user,
		AccessToken:          accessToken,
		RefreshToken:         newRefreshToken,
		AccessTokenExpiresAt: accessExpiresAt,
		RefreshTokenExpires:  refreshExpiresAt,
	}, nil
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return fmt.Errorf("%w: refresh_token is required", ErrInvalidInput)
	}

	tokenHash := hashToken(refreshToken)
	_, err := s.db.Exec(ctx, `
		UPDATE refresh_tokens
		SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE token_hash = $1
	`, tokenHash)
	if err != nil {
		return fmt.Errorf("logout revoke token: %w", err)
	}
	return nil
}

func (s *Service) GetUserByID(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.db.QueryRow(ctx, `
		SELECT id, email, username, display_name, created_at, updated_at
		FROM users
		WHERE id = $1 AND is_active = TRUE
	`, userID).Scan(
		&user.ID,
		&user.Email,
		&user.Username,
		&user.DisplayName,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}

func (s *Service) ValidateAccessToken(tokenString string) (string, error) {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return "", ErrInvalidToken
	}

	claims := &AccessClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method == nil || token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, ErrInvalidToken
			}
			return s.jwtSecret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrTokenExpired
		}
		return "", ErrInvalidToken
	}

	if !token.Valid || claims.TokenType != accessTokenType || strings.TrimSpace(claims.Subject) == "" {
		return "", ErrInvalidToken
	}

	return claims.Subject, nil
}

func (s *Service) issueAccessToken(userID string) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(s.accessTTL)
	jwtID, err := generateRandomToken(18)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate jwt id: %w", err)
	}

	claims := AccessClaims{
		TokenType: accessTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        jwtID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}

	return tokenString, expiresAt, nil
}

func (s *Service) insertRefreshToken(ctx context.Context, q queryRower, userID string, meta ClientMeta) (string, string, time.Time, error) {
	rawToken, err := generateRandomToken(48)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}

	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().UTC().Add(s.refreshTTL)

	var tokenID string
	err = q.QueryRow(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at, user_agent, ip_address)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, userID, tokenHash, expiresAt, nullableString(meta.UserAgent), nullableString(meta.IPAddress)).Scan(&tokenID)
	if err != nil {
		return "", "", time.Time{}, err
	}

	return rawToken, tokenID, expiresAt, nil
}

func mapUniqueConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("insert user: %w", err)
	}
	if pgErr.Code != "23505" {
		return fmt.Errorf("insert user: %w", err)
	}

	switch pgErr.ConstraintName {
	case "users_email_key":
		return ErrEmailExists
	case "users_username_key":
		return ErrUsernameExists
	default:
		return fmt.Errorf("insert user: %w", err)
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func generateRandomToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
