package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CloudOptions locates the operator's state for the flows below.
type CloudOptions struct {
	// CredentialsPath is ~/.orch/credentials outside tests.
	CredentialsPath string
	// ProjectID is the contract-shaped id (see CloudProjectID).
	ProjectID string
	// Getenv is os.Getenv outside tests.
	Getenv func(string) string
}

func (o CloudOptions) getenv(k string) string {
	if o.Getenv == nil {
		return os.Getenv(k)
	}
	return o.Getenv(k)
}

// CloudPublishResult is what one `orch publish --to cloud` did.
type CloudPublishResult struct {
	Upload CloudUpload
	// Created is true when this publish registered the project.
	Created bool
	// ViewerURL is empty when the view token is not known here (a CI
	// publish through the env override).
	ViewerURL string
	FromEnv   bool
}

// PublishToCloud uploads the export in dir to the project, registering the
// project first if this machine has no publish token for it.
//
// The site is read, and the contract's limits checked, before any request:
// a first publish that fails on a too-large file must not leave behind a
// registered project whose tokens were never used.
func PublishToCloud(ctx context.Context, dir string, o CloudOptions) (CloudPublishResult, error) {
	var res CloudPublishResult

	site, err := ReadCloudSite(dir)
	if err != nil {
		return res, err
	}

	cf, err := LoadCredentials(o.CredentialsPath)
	if err != nil {
		return res, err
	}
	target, err := ResolveCloudTarget(cf, o.ProjectID, o.getenv)
	if err != nil {
		return res, err
	}
	res.FromEnv = target.FromEnv
	client, err := NewCloudClient(target.URL)
	if err != nil {
		return res, err
	}

	if target.PublishToken == "" {
		created, err := registerProject(ctx, client, &cf, o, target)
		if err != nil {
			return res, err
		}
		target.PublishToken, target.ViewToken = created.PublishToken, created.ViewToken
		res.Created = true
	}

	up, err := client.UploadSite(ctx, target.PublishToken, o.ProjectID, site)
	if err != nil {
		return res, err
	}
	res.Upload = up
	if target.ViewToken != "" {
		res.ViewerURL = client.ViewerURL(target.ViewToken)
	}
	return res, nil
}

// registerProject creates the project with the admin token and stores its
// tokens before they are used.
func registerProject(ctx context.Context, client *CloudClient, cf *CredentialsFile,
	o CloudOptions, target CloudTarget) (CloudProjectTokens, error) {
	if target.AdminToken == "" {
		return CloudProjectTokens{}, fmt.Errorf(
			"this machine has no publish token for project %q and no admin token to create one — "+
				"run `orch cloud login --url %s` with the Worker's ADMIN_TOKEN on stdin",
			o.ProjectID, target.URL)
	}
	// The tokens a create returns exist nowhere else afterwards. Prove the
	// credentials file can be written BEFORE asking for them, so an
	// unwritable ~/.orch fails here instead of after the Worker has
	// registered a project nobody holds a token for.
	if err := SaveCredentials(o.CredentialsPath, *cf); err != nil {
		return CloudProjectTokens{}, fmt.Errorf("the credentials file must be writable before a project "+
			"is created: %w", err)
	}

	created, err := client.CreateProject(ctx, target.AdminToken, o.ProjectID)
	if IsCloudCode(err, "conflict") {
		return CloudProjectTokens{}, fmt.Errorf(
			"project %q already exists on %s, but this machine has no publish token for it "+
				"(published from another machine, or the credentials file was replaced) — "+
				"`orch cloud rotate --publish` issues a new publish token, then `orch cloud rotate` "+
				"a new viewer link, since the old view token cannot be recovered: %w",
			o.ProjectID, client.URL(), err)
	}
	if err != nil {
		return CloudProjectTokens{}, err
	}

	setProject(cf, o.ProjectID, CloudProject{PublishToken: created.PublishToken, ViewToken: created.ViewToken})
	if err := SaveCredentials(o.CredentialsPath, *cf); err != nil {
		// The one place a token is printed outside the viewer URL: the
		// alternative is losing it for good. The file was writable a moment
		// ago, so this is rare — but rare is not never.
		return CloudProjectTokens{}, fmt.Errorf(
			"project %q was created but its tokens could not be saved (%v). Keep these, they are "+
				"not shown again: publish token %s, viewer %s",
			o.ProjectID, err, created.PublishToken, client.ViewerURL(created.ViewToken))
	}
	return created, nil
}

func setProject(cf *CredentialsFile, id string, p CloudProject) {
	if cf.Cloud.Projects == nil {
		cf.Cloud.Projects = map[string]CloudProject{}
	}
	cf.Cloud.Projects[id] = p
}

// CloudLogin verifies adminToken against the Worker at url and stores both.
//
// Nothing is written unless the Worker accepts the token: a typo'd token
// saved and discovered at the first publish is a worse failure than one
// refused here. Switching to a different Worker drops the stored project
// tokens, which belong to the old one; it returns how many were dropped so
// the operator is told.
func CloudLogin(ctx context.Context, credentialsPath, url, adminToken string) (dropped int, err error) {
	adminToken = strings.TrimSpace(adminToken)
	if adminToken == "" {
		return 0, errors.New("no admin token on stdin — pipe the Worker's ADMIN_TOKEN, e.g. " +
			"`printf %s \"$ADMIN_TOKEN\" | orch cloud login --url <worker URL>`")
	}
	client, err := NewCloudClient(url)
	if err != nil {
		return 0, err
	}
	if _, err := client.Health(ctx); err != nil {
		return 0, err
	}
	if err := client.WhoAmI(ctx, adminToken); err != nil {
		return 0, err
	}

	cf, err := LoadCredentials(credentialsPath)
	if err != nil {
		return 0, err
	}
	next := &CloudCredentials{URL: client.URL(), AdminToken: adminToken}
	if cf.Cloud != nil {
		if cf.Cloud.URL == client.URL() {
			next.Projects = cf.Cloud.Projects
		} else {
			dropped = len(cf.Cloud.Projects)
		}
	}
	cf.Cloud = next
	return dropped, SaveCredentials(credentialsPath, cf)
}

// CloudLogout removes the cloud block, reporting whether there was one.
func CloudLogout(credentialsPath string) (bool, error) {
	cf, err := LoadCredentials(credentialsPath)
	if err != nil {
		return false, err
	}
	if cf.Cloud == nil {
		return false, nil
	}
	cf.Cloud = nil
	return true, SaveCredentials(credentialsPath, cf)
}

// storedCloud loads the credentials and requires a cloud block.
func storedCloud(o CloudOptions) (CredentialsFile, *CloudClient, error) {
	cf, err := LoadCredentials(o.CredentialsPath)
	if err != nil {
		return cf, nil, err
	}
	if cf.Cloud == nil || cf.Cloud.URL == "" {
		return cf, nil, errors.New("no orch-cloud Worker configured — run `orch cloud login --url <worker URL>`")
	}
	client, err := NewCloudClient(cf.Cloud.URL)
	return cf, client, err
}

// RotateCloudViewToken issues a new viewer link for the project and stores
// its token, returning the new URL. The old link stops working at once.
func RotateCloudViewToken(ctx context.Context, o CloudOptions) (string, error) {
	cf, client, err := storedCloud(o)
	if err != nil {
		return "", err
	}
	p := cf.Cloud.Projects[o.ProjectID]
	if p.PublishToken == "" {
		return "", fmt.Errorf("this machine has no publish token for project %q — "+
			"`orch cloud rotate --publish` issues one (or `orch publish --to cloud` creates the project)",
			o.ProjectID)
	}
	view, err := client.RotateViewToken(ctx, p.PublishToken, o.ProjectID)
	if err != nil {
		return "", err
	}
	p.ViewToken = view
	setProject(&cf, o.ProjectID, p)
	if err := SaveCredentials(o.CredentialsPath, cf); err != nil {
		return "", fmt.Errorf("the view token was rotated but not saved (%v); the new viewer link is %s",
			err, client.ViewerURL(view))
	}
	return client.ViewerURL(view), nil
}

// RotateCloudPublishToken re-issues the project's publish token with the
// admin token and stores it. The token itself is never returned: the only
// place it is needed is the credentials file it was just written to.
func RotateCloudPublishToken(ctx context.Context, o CloudOptions) error {
	cf, client, err := storedCloud(o)
	if err != nil {
		return err
	}
	if cf.Cloud.AdminToken == "" {
		return errors.New("re-issuing a publish token needs the admin token — run `orch cloud login --url " +
			cf.Cloud.URL + "` first")
	}
	pub, err := client.RotatePublishToken(ctx, cf.Cloud.AdminToken, o.ProjectID)
	if IsCloudCode(err, "not_found") {
		return fmt.Errorf("project %q does not exist on %s — `orch publish --to cloud` creates it: %w",
			o.ProjectID, client.URL(), err)
	}
	if err != nil {
		return err
	}
	p := cf.Cloud.Projects[o.ProjectID]
	p.PublishToken = pub
	setProject(&cf, o.ProjectID, p)
	if err := SaveCredentials(o.CredentialsPath, cf); err != nil {
		return fmt.Errorf("the publish token was rotated but could not be saved (%v); keep it: %s", err, pub)
	}
	return nil
}
