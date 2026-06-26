package accesstoken

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
	appstore "github.com/chenxuan520/agentbot/internal/store"
)

const ScopeProject = "project"
const ScopeSession = "session"
const sessionTokenPrefix = "abt_sess_"
const randomTokenBytes = 32

type Scope struct {
	Kind string
	Ref  conversation.Ref
}

type Service struct {
	store        appstore.SessionTokenStore
	projectToken string
	key          []byte
}

func NewService(store appstore.SessionTokenStore, projectToken, secret string) *Service {
	service := &Service{store: store, projectToken: strings.TrimSpace(projectToken)}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return service
	}
	sum := sha256.Sum256([]byte(secret))
	service.key = append([]byte(nil), sum[:]...)
	return service
}

func (s *Service) Validate(token string) (*Scope, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if s.projectToken != "" && token == s.projectToken {
		return &Scope{Kind: ScopeProject}, nil
	}
	if s.store == nil {
		return nil, fmt.Errorf("session token store is not configured")
	}
	record, err := s.store.GetSessionTokenByHash(hashToken(token))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("invalid token")
	}
	return &Scope{
		Kind: ScopeSession,
		Ref:  conversation.Ref{Provider: record.Provider, ConversationID: record.ConversationID},
	}, nil
}

func (s *Service) SessionToken(ref conversation.Ref) (string, bool, error) {
	if s.store == nil {
		return "", false, fmt.Errorf("session token store is not configured")
	}
	record, err := s.store.GetSessionToken(ref)
	if err != nil {
		return "", false, err
	}
	if record == nil {
		return "", false, nil
	}
	token, err := s.decrypt(record.TokenCiphertext)
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

func (s *Service) EnsureSessionToken(ref conversation.Ref) (string, error) {
	token, ok, err := s.SessionToken(ref)
	if err != nil {
		return "", err
	}
	if ok {
		return token, nil
	}
	return s.RotateSessionToken(ref)
}

func (s *Service) RotateSessionToken(ref conversation.Ref) (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("session token store is not configured")
	}
	if len(s.key) == 0 {
		return "", fmt.Errorf("auth secret is not configured")
	}

	now := time.Now().UTC()
	existing, err := s.store.GetSessionToken(ref)
	if err != nil {
		return "", err
	}
	createdAt := now
	if existing != nil && !existing.CreatedAt.IsZero() {
		createdAt = existing.CreatedAt
	}

	for attempt := 0; attempt < 10; attempt++ {
		token, err := generateRandomSessionToken()
		if err != nil {
			return "", err
		}
		tokenHash := hashToken(token)
		clash, err := s.store.GetSessionTokenByHash(tokenHash)
		if err != nil {
			return "", err
		}
		if clash != nil && (clash.Provider != ref.Provider || clash.ConversationID != ref.ConversationID) {
			continue
		}
		ciphertext, err := s.encrypt(token)
		if err != nil {
			return "", err
		}
		if err := s.store.UpsertSessionToken(appstore.SessionTokenRecord{
			Provider:        ref.Provider,
			ConversationID:  ref.ConversationID,
			TokenHash:       tokenHash,
			TokenCiphertext: ciphertext,
			CreatedAt:       createdAt,
			UpdatedAt:       now,
		}); err != nil {
			return "", err
		}
		return token, nil
	}

	return "", fmt.Errorf("failed to generate unique session token")
}

func generateRandomSessionToken() (string, error) {
	raw := make([]byte, randomTokenBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	// The token is an opaque string (only ever hashed / encrypted, never
	// base64-decoded), so collapse the URL-safe '-' into '_' to keep the whole
	// token a single double-click-selectable word.
	encoded := strings.ReplaceAll(base64.RawURLEncoding.EncodeToString(raw), "-", "_")
	return sessionTokenPrefix + encoded, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Service) encrypt(plaintext string) (string, error) {
	if len(s.key) == 0 {
		return "", fmt.Errorf("auth secret is not configured")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, sealed...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (s *Service) decrypt(ciphertext string) (string, error) {
	if len(s.key) == 0 {
		return "", fmt.Errorf("auth secret is not configured")
	}
	data, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("invalid token ciphertext")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
