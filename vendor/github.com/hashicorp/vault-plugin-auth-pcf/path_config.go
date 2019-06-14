package pcf

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/vault-plugin-auth-pcf/models"
	"github.com/hashicorp/vault-plugin-auth-pcf/util"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const configStorageKey = "config"

func (b *backend) pathConfig() *framework.Path {
	return &framework.Path{
		Pattern: "config",
		Fields: map[string]*framework.FieldSchema{
			"identity_ca_certificates": {
				Required:     true,
				Type:         framework.TypeStringSlice,
				DisplayName:  "Identity CA Certificates",
				DisplayValue: `-----BEGIN CERTIFICATE----- ... -----END CERTIFICATE-----`,
				Description:  "The PEM-format CA certificates that are required to have issued the instance certificates presented for logging in.",
			},
			"pcf_api_trusted_certificates": {
				Type:         framework.TypeStringSlice,
				DisplayName:  "PCF API Trusted IdentityCACertificates",
				DisplayValue: `-----BEGIN CERTIFICATE----- ... -----END CERTIFICATE-----`,
				Description:  "The PEM-format CA certificates that are acceptable for the PCF API to present.",
			},
			"pcf_api_addr": {
				Required:     true,
				Type:         framework.TypeString,
				DisplayName:  "PCF API Address",
				DisplayValue: "https://api.10.244.0.34.xip.io",
				Description:  "PCF’s API address.",
			},
			"pcf_username": {
				Required:     true,
				Type:         framework.TypeString,
				DisplayName:  "PCF API Username",
				DisplayValue: "admin",
				Description:  "The username for PCF’s API.",
			},
			"pcf_password": {
				Required:         true,
				Type:             framework.TypeString,
				DisplayName:      "PCF API Password",
				DisplayValue:     "admin",
				Description:      "The password for PCF’s API.",
				DisplaySensitive: true,
			},
			"login_max_minutes_old": {
				Type:         framework.TypeInt,
				DisplayName:  "Login Max Minutes Old",
				DisplayValue: "5",
				Description: `When logging in, the max number of minutes in the past the "signing_time" can be. Useful for clock drift. 
Set low to reduce opportunities for replay attacks.`,
				Default: 5,
			},
			"login_max_minutes_ahead": {
				Type:         framework.TypeInt,
				DisplayName:  "Login Max Minutes Ahead",
				DisplayValue: "1",
				Description:  `When logging in, the max number of minutes in the future the "signing_time" can be. Useful for clock drift.`,
				Default:      1,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{
				Callback: b.operationConfigCreateUpdate,
			},
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.operationConfigCreateUpdate,
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.operationConfigRead,
			},
			logical.DeleteOperation: &framework.PathOperation{
				Callback: b.operationConfigDelete,
			},
		},
		HelpSynopsis:    pathConfigSyn,
		HelpDescription: pathConfigDesc,
	}
}

func (b *backend) operationConfigCreateUpdate(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	config, err := config(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if config == nil {
		// They're creating a config.
		identityCACerts := data.Get("identity_ca_certificates").([]string)
		if len(identityCACerts) == 0 {
			return logical.ErrorResponse("'certificates' is required"), nil
		}
		pcfApiAddr := data.Get("pcf_api_addr").(string)
		if pcfApiAddr == "" {
			return logical.ErrorResponse("'pcf_api_addr' is required"), nil
		}
		pcfUsername := data.Get("pcf_username").(string)
		if pcfUsername == "" {
			return logical.ErrorResponse("'pcf_username' is required"), nil
		}
		pcfPassword := data.Get("pcf_password").(string)
		if pcfPassword == "" {
			return logical.ErrorResponse("'pcf_password' is required"), nil
		}
		pcfApiCertificates := data.Get("pcf_api_trusted_certificates").([]string)

		// Default this to 5 minutes.
		loginMaxMinOld := 5
		loginMaxMinOldIfc, ok := data.GetOk("login_max_minutes_old")
		if ok {
			loginMaxMinOld = loginMaxMinOldIfc.(int)
		}

		// Default this to 1 minute.
		loginMaxMinAhead := 1
		loginMaxMinAheadIfc, ok := data.GetOk("login_max_minutes_ahead")
		if ok {
			loginMaxMinAhead = loginMaxMinAheadIfc.(int)
		}
		config = &models.Configuration{
			IdentityCACertificates: identityCACerts,
			PCFAPICertificates:     pcfApiCertificates,
			PCFAPIAddr:             pcfApiAddr,
			PCFUsername:            pcfUsername,
			PCFPassword:            pcfPassword,
			LoginMaxMinOld:         loginMaxMinOld,
			LoginMaxMinAhead:       loginMaxMinAhead,
		}
	} else {
		// They're updating a config. Only update the fields that have been sent in the call.
		if raw, ok := data.GetOk("identity_ca_certificates"); ok {
			switch v := raw.(type) {
			case []interface{}:
				certificates := make([]string, len(v))
				for _, certificateIfc := range v {
					certificate, ok := certificateIfc.(string)
					if !ok {
						continue
					}
					certificates = append(certificates, certificate)
				}
				config.IdentityCACertificates = certificates
			case string:
				config.IdentityCACertificates = []string{v}
			}
		}
		if raw, ok := data.GetOk("pcf_api_trusted_certificates"); ok {
			switch v := raw.(type) {
			case []interface{}:
				pcfAPICertificates := make([]string, len(v))
				for _, certificateIfc := range v {
					certificate, ok := certificateIfc.(string)
					if !ok {
						continue
					}
					pcfAPICertificates = append(pcfAPICertificates, certificate)
				}
				config.PCFAPICertificates = pcfAPICertificates
			case string:
				config.PCFAPICertificates = []string{v}
			}
		}
		if raw, ok := data.GetOk("pcf_api_addr"); ok {
			config.PCFAPIAddr = raw.(string)
		}
		if raw, ok := data.GetOk("pcf_username"); ok {
			config.PCFUsername = raw.(string)
		}
		if raw, ok := data.GetOk("pcf_password"); ok {
			config.PCFPassword = raw.(string)
		}
		if raw, ok := data.GetOk("login_max_minutes_old"); ok {
			config.LoginMaxMinOld = raw.(int)
		}
		if raw, ok := data.GetOk("login_max_minutes_ahead"); ok {
			config.LoginMaxMinAhead = raw.(int)
		}
	}

	// To give early and explicit feedback, make sure the config works by executing a test call
	// and checking that the API version is supported. If they don't have API v2 running, we would
	// probably expect a timeout of some sort below because it's first called in the NewPCFClient
	// method.
	client, err := util.NewPCFClient(config)
	if err != nil {
		return nil, fmt.Errorf("unable to establish an initial connection to the PCF API: %s", err)
	}
	info, err := client.GetInfo()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(info.APIVersion, "2.") {
		return nil, fmt.Errorf("the PCF auth plugin only supports version 2.X.X of the PCF API")
	}

	entry, err := logical.StorageEntryJSON(configStorageKey, config)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *backend) operationConfigRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	config, err := config(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, nil
	}
	return &logical.Response{
		Data: map[string]interface{}{
			"identity_ca_certificates":     config.IdentityCACertificates,
			"pcf_api_trusted_certificates": config.PCFAPICertificates,
			"pcf_api_addr":                 config.PCFAPIAddr,
			"pcf_username":                 config.PCFUsername,
			"login_max_minutes_old":        config.LoginMaxMinOld,
			"login_max_minutes_ahead":      config.LoginMaxMinAhead,
		},
	}, nil
}

func (b *backend) operationConfigDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	if err := req.Storage.Delete(ctx, configStorageKey); err != nil {
		return nil, err
	}
	return nil, nil
}

// storedConfig may return nil without error if the user doesn't currently have a config.
func config(ctx context.Context, storage logical.Storage) (*models.Configuration, error) {
	entry, err := storage.Get(ctx, configStorageKey)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	config := &models.Configuration{}
	if err := entry.DecodeJSON(config); err != nil {
		return nil, err
	}
	return config, nil
}

const pathConfigSyn = `
Provide Vault with the CA certificate used to issue all client certificates.
`

const pathConfigDesc = `
When a login is attempted using a PCF client certificate, Vault will verify
that the client certificate was issued by the CA certificate configured here.
Only those passing this check will be able to gain authorization.
`
