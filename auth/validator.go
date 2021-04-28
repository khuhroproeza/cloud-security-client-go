// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company and Cloud Security Client Go contributors
//
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jws"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/sap/cloud-security-client-go/oidcclient"
	"net/url"
	"strings"
	"time"
)

// parseAndValidateJWT parses the token into its claims, verifies the claims and verifies the signature
func (m *Middleware) parseAndValidateJWT(rawToken string) (Token, error) {

	token, err := NewToken(rawToken)
	if err != nil {
		return nil, err
	}

	// get keyset
	keySet, err := m.getOIDCTenant(token.Issuer())
	if err != nil {
		return nil, err
	}

	// verify claims
	if err := m.validateClaims(token, keySet); err != nil {
		return nil, err
	}

	// verify signature
	if err := m.verifySignature(token, keySet); err != nil {
		return nil, err
	}

	return token, nil
}

func (m *Middleware) verifySignature(t Token, keySet *oidcclient.OIDCTenant) (err error) {
	headers, err := getHeaders(t.GetTokenValue())
	if err != nil {
		return err
	}
	kid := headers.KeyID()
	alg := headers.Algorithm()

	//fail early to avoid another parsing of encoded token
	if alg == "" {
		return errors.New("alg is missing from jwt header")
	}

	publicKey, err := getPublicKey(kid, keySet)
	if err != nil {
		return err
	}

	//Parse and verify signature
	_, err = jwt.ParseString(t.GetTokenValue(), jwt.WithVerify(alg, publicKey))
	if err != nil {
		return err
	}
	return nil
}

func getPublicKey(kid string, keySet *oidcclient.OIDCTenant) (jwk.Key, error) {
	jwks, _ := keySet.GetJWKs()
	var jsonWebKey *oidcclient.JSONWebKey
	if kid != "" {
		for _, key := range jwks {
			if key.Kid == kid {
				jsonWebKey = key
				break
			}
		}
		if jsonWebKey == nil {
			return nil, fmt.Errorf("token is unverifiable: 'kid' is specified in token, but no jwk provided by server")
		}
	} else if len(jwks) == 1 {
		jsonWebKey = jwks[0]
	} else {
		return nil, fmt.Errorf("token is unverifiable: no kid specified in token and more than one jwk available from server")
	}

	pubKey, _ := jwk.New(jsonWebKey.Key)
	pubKey.Set(jwk.KeyIDKey, jsonWebKey.Kid)
	pubKey.Set(jwk.KeyTypeKey, jsonWebKey.Kty)
	return pubKey, nil
}

func getHeaders(encodedToken string) (jws.Headers, error) {
	msg, err := jws.Parse([]byte(encodedToken))
	if err != nil {
		return nil, err
	}

	return msg.Signatures()[0].ProtectedHeaders(), nil
}

func (m *Middleware) validateClaims(t Token, ks *oidcclient.OIDCTenant) error {

	//performing IsExpired check, because dgriljalva jwt.Validate() doesn't fail on missing 'exp' claim
	if t.IsExpired() {
		return fmt.Errorf("token is expired, exp: %v", t.Expiration())
	}
	err := jwt.Validate(t.getJwtToken(),
		jwt.WithAudience(m.oAuthConfig.GetClientID()),
		jwt.WithIssuer(ks.ProviderJSON.Issuer),
		jwt.WithAcceptableSkew(1*time.Minute)) //to keep leeway in sync with Token.IsExpired

	if err != nil {
		return fmt.Errorf("claim validation failed: %v", err)
	}
	return nil
}

func (m *Middleware) getOIDCTenant(tokenIssuer string) (*oidcclient.OIDCTenant, error) {
	issURI, err := m.verifyIssuer(tokenIssuer)
	if err != nil {
		return nil, err
	}

	oidcTenant, exp, found := m.oidcTenants.GetWithExpiration(tokenIssuer)
	if !found || time.Now().After(exp) {
		newKeySet, err, _ := m.sf.Do(tokenIssuer, func() (i interface{}, err error) {
			set, err := oidcclient.NewOIDCTenant(m.options.HTTPClient, issURI)
			return set, err
		})

		if err != nil {
			return nil, fmt.Errorf("token is unverifiable: unable to perform oidc discovery: %v", err)
		}
		oidcTenant = newKeySet.(*oidcclient.OIDCTenant)
		m.oidcTenants.SetDefault(oidcTenant.(*oidcclient.OIDCTenant).ProviderJSON.Issuer, oidcTenant)
	}
	return oidcTenant.(*oidcclient.OIDCTenant), nil
}

func (m *Middleware) verifyIssuer(issuer string) (issUri *url.URL, err error) {
	issURI, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("unable to parse Issuer URI: %s", issuer)
	}

	if !strings.HasSuffix(issURI.Host, m.oAuthConfig.GetDomain()) {
		return nil, fmt.Errorf("token is unverifiable: unknown server (domain doesn't match)")
	}
	return issURI, nil
}
