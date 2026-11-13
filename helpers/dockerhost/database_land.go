package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"

	"github.com/mitstack-eu/block-kit/capabilities/github/ghapi"
	"github.com/mitstack-eu/block-kit/contract"
	"github.com/mitstack-eu/block-kit/handler"
)

const (
	// maxCommitAttempts bounds the re-reads when another app lands on the host between a read and its commit.
	maxCommitAttempts = 3
	// foreignOwner stands for the owner of a compose file without the block's subscription label.
	foreignOwner = "a compose file this block did not write"
)

// EnsureResources writes the database's files into its app's directory on subscribe and upgrade, once every check
// passes, so a refusal writes nothing.
func (db Database) EnsureResources(ctx context.Context, d contract.Desired) ([]contract.CreatedResource, error) {
	r, err := readRequest(d)
	if err != nil {
		return nil, err
	}
	repo, err := db.dial(d)
	if err != nil {
		return nil, err
	}
	if err := retryOnMovedMain(func() error { return db.land(ctx, d, repo, r) }); err != nil {
		return nil, err
	}
	log.Printf("%s: subscription %s holds %s's database in %s, reserving %s", db.Block, r.subID, r.app, repo.fullName, Describe(r.size))
	return []contract.CreatedResource{{Name: DatabaseResource, Type: db.Type, Identity: db.identity(repo, r.target)}}, nil
}

// Ensure completes handler.Resource, and the handler calls EnsureResources in its place.
func (db Database) Ensure(ctx context.Context, d contract.Desired) error {
	_, err := db.EnsureResources(ctx, d)
	return err
}

// retryOnMovedMain reruns a read-then-commit whose commit found main moved, so every check reads the newest state.
func retryOnMovedMain(run func() error) error {
	var err error
	for range maxCommitAttempts {
		if err = run(); !errors.Is(err, ghapi.ErrNotFastForward) {
			return err
		}
	}
	return fmt.Errorf("main of the host's repository moved under each of %d attempts: %w", maxCommitAttempts, err)
}

func (db Database) land(ctx context.Context, d contract.Desired, repo *hostRepo, r request) error {
	snap, err := repo.snapshot(ctx)
	if err != nil {
		return err
	}
	owner, err := db.owner(ctx, repo, snap, r.app)
	if err != nil {
		return err
	}
	if heldByAnother(owner, r.target) {
		return handler.Refuse("app-taken", "%s/%s belongs to the subscription %s, so %s cannot give the app a second database", r.app, db.fileCompose(), owner, r.subID)
	}
	if err := db.refuseResize(ctx, repo, snap, r); err != nil {
		return err
	}
	others, err := otherReservations(ctx, repo, snap, appPath(r.app, db.fileReservation()))
	if err != nil {
		return err
	}
	if err := db.checkFits(ctx, d, r, others); err != nil {
		return err
	}
	files, err := db.renderFiles(r)
	if err != nil {
		return err
	}
	changes, err := changedFiles(ctx, repo, snap, files)
	if err != nil {
		return err
	}
	// A request left by an unsubscribe the database outlived names a backup older than the data a later one must keep.
	if p := appPath(r.app, db.fileRequest()); snap.paths[p] {
		changes[p] = ""
	}
	if len(changes) == 0 {
		return nil
	}
	return repo.commit(ctx, snap.head, db.commitMessage("lands", r.target), changes)
}

// changedFiles keeps the files whose content differs from what main holds, so an unchanged upgrade commits nothing.
func changedFiles(ctx context.Context, repo *hostRepo, snap snapshot, files map[string]string) (map[string]string, error) {
	changes := map[string]string{}
	for p, content := range files {
		if !snap.paths[p] {
			changes[p] = content
			continue
		}
		held, err := repo.read(ctx, snap.head, p)
		if err != nil {
			return nil, err
		}
		if held != content {
			changes[p] = content
		}
	}
	return changes, nil
}

func heldByAnother(owner string, t target) bool { return owner != "" && owner != t.subID }

func (db Database) commitMessage(verb string, t target) string {
	return fmt.Sprintf("%s %s %s's database\n\nMitstack-Subscription: %s\n", db.Block, verb, t.app, t.subID)
}

// owner is the subscription the database's compose file names, "" when the directory holds none.
func (db Database) owner(ctx context.Context, repo *hostRepo, snap snapshot, app string) (string, error) {
	p := appPath(app, db.fileCompose())
	if !snap.paths[p] {
		return "", nil
	}
	text, err := repo.read(ctx, snap.head, p)
	if err != nil {
		return "", err
	}
	var c Compose
	if err := json.Unmarshal([]byte(text), &c); err != nil || c.Services[db.Engine.Service].Labels[labelSub] == "" {
		return foreignOwner, nil
	}
	return c.Services[db.Engine.Service].Labels[labelSub], nil
}

// refuseResize refuses a disk other than the volume's, since the host makes the volume once at its first size.
func (db Database) refuseResize(ctx context.Context, repo *hostRepo, snap snapshot, r request) error {
	p := appPath(r.app, db.fileReservation())
	if !snap.paths[p] {
		return nil
	}
	text, err := repo.read(ctx, snap.head, p)
	if err != nil {
		return err
	}
	var held Reservation
	if err := json.Unmarshal([]byte(text), &held); err != nil {
		return fmt.Errorf("reading %s: %w", p, err)
	}
	if held.DiskBytes != 0 && held.DiskBytes != r.size.DiskBytes {
		return handler.Refuse("disk-fixed", "%s's database volume was made at %s, and the host cannot resize it to %s; keep params[%q] at %s",
			r.app, sizeText(held.DiskBytes), sizeText(r.size.DiskBytes), paramDisk, strings.TrimSuffix(sizeText(held.DiskBytes), "B"))
	}
	return nil
}

func otherReservations(ctx context.Context, repo *hostRepo, snap snapshot, own string) (Reservation, error) {
	files := map[string][]byte{}
	for p := range snap.paths {
		if p == own || path.Base(path.Dir(p)) != ReservationDir {
			continue
		}
		text, err := repo.read(ctx, snap.head, p)
		if err != nil {
			return Reservation{}, err
		}
		files[p] = []byte(text)
	}
	return SumReservations(files, own)
}

// checkFits waits for a host that has reported no capacity, and refuses a host that takes no backups or a database
// whose reservation does not fit in what the host can give its apps less what the others hold.
func (db Database) checkFits(ctx context.Context, d contract.Desired, r request, others Reservation) error {
	report, found, err := db.readReport(ctx, d, r.hostSub)
	if err != nil {
		return err
	}
	ask := Ask{Part: r.app + "'s database", Kind: "database", Size: r.size}
	allocatable, err := Capacity(report.Report, found, ask.Part)
	if err != nil {
		return err
	}
	if reason := report.Host.Backups.Error; reason != nil {
		return handler.Refuse("host-takes-no-backups",
			"the host reports %q, so a database on it could never be backed up nor removed after a final backup; give the database a host made with a backup public key", *reason)
	}
	return fitRoom(allocatable, ask, others)
}

// Delete asks the host for a final backup of a database that holds data, waits for the host to report it uploaded,
// and only then removes the database's files, which takes its container and volume off the host. It deletes no backup.
func (db Database) Delete(ctx context.Context, d contract.Desired) (map[string]string, error) {
	t, err := readTarget(d)
	if err != nil {
		return nil, err
	}
	repo, err := db.dial(d)
	if err != nil {
		return nil, err
	}
	return nil, retryOnMovedMain(func() error { return db.remove(ctx, d, repo, t) })
}

func (db Database) remove(ctx context.Context, d contract.Desired, repo *hostRepo, t target) error {
	snap, err := repo.snapshot(ctx)
	if err != nil {
		return err
	}
	owner, err := db.owner(ctx, repo, snap, t.app)
	if err != nil || heldByAnother(owner, t) {
		return err
	}
	if owner != "" {
		done, err := db.finalBackup(repo, snap, t).Done(ctx, db.Reports, d, t.hostSub)
		if err != nil || !done {
			return err
		}
	}
	removals := map[string]string{}
	for _, name := range db.ownedFiles() {
		if p := appPath(t.app, name); snap.paths[p] {
			removals[p] = ""
		}
	}
	if len(removals) == 0 {
		return nil
	}
	if err := repo.commit(ctx, snap.head, db.commitMessage("removes", t), removals); err != nil {
		return err
	}
	log.Printf("%s: subscription %s holds no file of %s's database in %s", db.Block, t.subID, t.app, repo.fullName)
	return nil
}

func (db Database) finalBackup(repo *hostRepo, snap snapshot, t target) FinalBackup {
	p := appPath(t.app, db.fileRequest())
	return FinalBackup{
		App: t.app, Block: db.Block, Kept: t.app + "'s database", Now: db.now(),
		Held: func(ctx context.Context) (string, error) {
			if !snap.paths[p] {
				return "", nil
			}
			return repo.read(ctx, snap.head, p)
		},
		Ask: func(ctx context.Context, request string) error {
			return repo.commit(ctx, snap.head, db.commitMessage("asks for a final backup of", t), map[string]string{p: request + "\n"})
		},
	}
}
