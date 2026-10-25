package connector

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

const LoginFlowPassword = "password"

func (bc *BitrixConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Password",
		Description: "Log in with your Bitrix24 portal URL, login and password",
		ID:          LoginFlowPassword,
	}}
}

func (bc *BitrixConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowPassword {
		return nil, fmt.Errorf("unknown login flow ID %q", flowID)
	}
	return &PasswordLogin{User: user, Main: bc}, nil
}

type PasswordLogin struct {
	User *bridgev2.User
	Main *BitrixConnector
}

var _ bridgev2.LoginProcessUserInput = (*PasswordLogin)(nil)

func (pl *PasswordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "kz.adil.bitrix.enter_credentials",
		Instructions: "Enter your Bitrix24 portal address and credentials",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:         bridgev2.LoginInputFieldTypeURL,
					ID:           "portal_url",
					Name:         "Portal URL",
					Description:  "e.g. https://portal.example.com",
					DefaultValue: pl.Main.Config.DefaultPortalURL,
				},
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   "login",
					Name: "Login",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   "password",
					Name: "Password",
				},
			},
		},
	}, nil
}

func (pl *PasswordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	client, err := bitrix.NewClient(input["portal_url"])
	if err != nil {
		return nil, err
	}
	host := client.BaseURL.Host
	// Ghost user IDs are plain Bitrix user IDs, so a bridge instance can only serve one portal.
	for _, existing := range pl.Main.br.GetAllCachedUserLogins() {
		if meta, ok := existing.Metadata.(*UserLoginMetadata); ok && hostOf(meta.PortalURL) != host {
			return nil, fmt.Errorf("this bridge is already used for %s; run a separate bridge instance per portal", hostOf(meta.PortalURL))
		}
	}
	if err = client.PasswordLogin(ctx, input["login"], input["password"]); errors.Is(err, bitrix.ErrInvalidLogin) {
		return nil, fmt.Errorf("%w (if the portal shows a captcha or 2FA, log in once in a browser first)", err)
	} else if err != nil {
		return nil, err
	}
	name := input["login"]
	if self, err := client.GetUser(ctx, client.UserID); err == nil && self.Name != "" {
		name = self.Name
	}
	ul, err := pl.User.NewLogin(ctx, &database.UserLogin{
		ID:         makeUserLoginID(host, client.UserID),
		RemoteName: name,
		RemoteProfile: status.RemoteProfile{
			Username: input["login"],
			Name:     name,
			Avatar:   networkIcon,
		},
		Metadata: &UserLoginMetadata{
			PortalURL: client.BaseURL.String(),
			Login:     input["login"],
			Password:  input["password"],
			UserID:    client.UserID,
			Cookies:   client.Cookies(),
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			login.Client = newBitrixClient(pl.Main, login, client)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	go ul.Client.Connect(ul.Log.WithContext(context.Background()))
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "kz.adil.bitrix.complete",
		Instructions: fmt.Sprintf("Successfully logged in as %s", name),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

func (pl *PasswordLogin) Cancel() {}
