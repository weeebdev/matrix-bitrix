package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/weeebdev/mautrix-bitrix/pkg/bitrix"
)

var _ bridgev2.UserSearchingNetworkAPI = (*BitrixClient)(nil)

func (bc *BitrixClient) resolveUser(ctx context.Context, user *bitrix.User, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	userID := makeUserID(int64(user.ID))
	ghost, err := bc.UserLogin.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: bc.userInfo(user),
	}
	if createChat {
		// DM portals are keyed by the other user's ID; the Bitrix chat itself is created on first send.
		resp.Chat = &bridgev2.CreateChatResponse{PortalKey: bc.portalKey(string(userID))}
	}
	return resp, nil
}

// ResolveIdentifier accepts a Bitrix user ID, or a name to search for when it matches exactly one person.
func (bc *BitrixClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if id, err := strconv.ParseInt(identifier, 10, 64); err == nil && id > 0 {
		user, err := bc.client.GetUser(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("failed to get user %d: %w", id, err)
		} else if user.ID == 0 {
			return nil, fmt.Errorf("user %d not found", id)
		}
		return bc.resolveUser(ctx, user, createChat)
	}
	users, err := bc.client.SearchUsers(ctx, identifier)
	if err != nil {
		return nil, err
	}
	switch len(users) {
	case 0:
		return nil, fmt.Errorf("no colleague found for %q", identifier)
	case 1:
		return bc.resolveUser(ctx, users[0], createChat)
	default:
		return nil, fmt.Errorf("%d colleagues match %q, be more specific or use their user ID", len(users), identifier)
	}
}

func (bc *BitrixClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	users, err := bc.client.SearchUsers(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]*bridgev2.ResolveIdentifierResponse, 0, len(users))
	for _, user := range users {
		if int64(user.ID) == bc.client.UserID {
			continue
		}
		resp, err := bc.resolveUser(ctx, user, false)
		if err != nil {
			return nil, err
		}
		out = append(out, resp)
	}
	return out, nil
}
