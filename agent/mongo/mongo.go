package mongo

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/juju/loggo"
	"labix.org/v2/mgo"

	"launchpad.net/juju-core/replicaset"
	"launchpad.net/juju-core/upstart"
	"launchpad.net/juju-core/utils"
)

const (
	maxFiles = 65000
	maxProcs = 20000

	replicaSetName = "juju"
)

var (
	logger = loggo.GetLogger("juju.agent.mongo")

	oldMongoServiceName = "juju-db"

	// JujuMongodPath holds the default path to the juju-specific mongod.
	JujuMongodPath = "/usr/lib/juju/bin/mongod"
)

// MongoPath returns the executable path to be used to run mongod on this
// machine. If the juju-bundled version of mongo exists, it will return that
// path, otherwise it will return the command to run mongod from the path.
func MongodPath() (string, error) {
	if _, err := os.Stat(JujuMongodPath); err == nil {
		return JujuMongodPath, nil
	}

	path, err := exec.LookPath("mongod")
	if err != nil {
		return "", err
	}
	return path, nil
}

// EnsureMongoServer ensures that the correct mongo upstart script is installed
// and running.
//
// This method will remove old versions of the mongo upstart script as necessary
// before installing the new version.
//
// This is a variable so it can be overridden in tests
var EnsureMongoServer = ensureMongoServer

func ensureMongoServer(address, dataDir string, port int, info *mgo.DialInfo) error {
	logger.Debugf("Ensuring mongo server is running.  address: %v, dir: %v, port: %v, dialinfo: %#v",
		address, dataDir, port, *info)
	dbDir := filepath.Join(dataDir, "db")
	name := makeServiceName(mongoScriptVersion)

	if err := removeOldMongoServices(mongoScriptVersion); err != nil {
		return err
	}

	service, err := mongoUpstartService(name, dataDir, dbDir, port)
	if err != nil {
		return err
	}

	if !service.Installed() {
		if err := makeJournalDirs(dbDir); err != nil {
			return fmt.Errorf("Error creating journal directories: %v", err)
		}

		logger.Debugf("mongod upstart command: %s", service.Cmd)
		err = service.Install()
		if err != nil {
			return fmt.Errorf("failed to install mongo service %q: %v", service.Name, err)
		}
	}

	if !service.Running() {
		if err := service.Start(); err != nil {
			return fmt.Errorf("failed to start %q service: %v", name, err)
		}
		logger.Infof("Mongod service %q started.", name)
	}

	if err := initiateReplicaSet(address, port, info); err != nil {
		logger.Debugf("Error initiating replicaset: %v", err)
		return fmt.Errorf("failed to initiate mongo replicaset: %v", err)
	}
	return nil
}

// initiateReplicaSet checks for an existing mongo configuration using CurrentConfig.
// If no existing configuration is found one is created using Initiate.
//
// This is a variable so it can be overridden in tests
var initiateReplicaSet = func(address string, port int, info *mgo.DialInfo) error {
	logger.Debugf("Initiating mongo replicaset with address %q, port %d, dialinfo %#v", address, port, *info)

	addrs, _ := net.InterfaceAddrs()
	logger.Debugf("Interface addresses: %#v", addrs)

	session, err := mgo.DialWithInfo(info)
	if err != nil {
		return fmt.Errorf("can't dial mongo to initiate replicaset: %v", err)
	}
	defer session.Close()

	_, err = replicaset.CurrentConfig(session)
	if err == mgo.ErrNotFound {
		address = fmt.Sprintf("%s:%d", address, port)
		err = replicaset.Initiate(session, address, replicaSetName)
		if err != nil {
			return fmt.Errorf("error from mongo initiate: %v", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("error getting replicaset config: %v", err)
	}
	return nil
}

func makeJournalDirs(dir string) error {
	journalDir := path.Join(dir, "journal")

	if err := os.MkdirAll(journalDir, 0700); err != nil {
		logger.Errorf("failed to make mongo journal dir %s: %v", journalDir, err)
		return err
	}

	// manually create the prealloc files, since otherwise they get created as 100M files.
	zeroes := make([]byte, 64*1024) // should be enough for anyone
	for x := 0; x < 3; x++ {
		name := fmt.Sprintf("prealloc.%d", x)
		filename := filepath.Join(journalDir, name)
		f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0700)
		if err != nil {
			return fmt.Errorf("failed to open mongo prealloc file %q: %v", filename, err)
		}
		defer f.Close()
		for total := 0; total < 1024*1024; {
			n, err := f.Write(zeroes)
			if err != nil {
				return fmt.Errorf("failed to write to mongo prealloc file %q: %v", filename, err)
			}
			total += n
		}
	}
	return nil
}

// removeOldMongoServices looks for any old juju mongo upstart scripts and
// removes them.
func removeOldMongoServices(curVersion int) error {
	old := upstart.NewService(oldMongoServiceName)
	if err := old.StopAndRemove(); err != nil {
		logger.Errorf("failed to remove old mongo upstart service %q: %v", old.Name, err)
		return err
	}

	// the new formatting for the script name started at version 2
	for x := 2; x < curVersion; x++ {
		old := upstart.NewService(makeServiceName(x))
		if err := old.StopAndRemove(); err != nil {
			logger.Errorf("failed to remove old mongo upstart service %q: %v", old.Name, err)
			return err
		}
	}
	return nil
}

func makeServiceName(version int) string {
	return fmt.Sprintf("juju-db-v%d", version)
}

// RemoveService will stop and remove Juju's mongo upstart service.
func RemoveService() error {
	svc := upstart.NewService(makeServiceName(mongoScriptVersion))
	return svc.StopAndRemove()
}

// mongoScriptVersion keeps track of changes to the mongo upstart script.
// Update this version when you update the script that gets installed from
// MongoUpstartService.
const mongoScriptVersion = 2

// mongoUpstartService returns the upstart config for the mongo state service.
//
// This method assumes there is a server.pem keyfile in dataDir.
func mongoUpstartService(name, dataDir, dbDir string, port int) (*upstart.Conf, error) {
	keyFile := path.Join(dataDir, "server.pem")
	svc := upstart.NewService(name)

	conf := &upstart.Conf{
		Service: *svc,
		Desc:    "juju state database",
		Limit: map[string]string{
			"nofile": fmt.Sprintf("%d %d", maxFiles, maxFiles),
			"nproc":  fmt.Sprintf("%d %d", maxProcs, maxProcs),
		},
		Cmd: "/usr/bin/mongod" +
			" --auth" +
			" --dbpath=" + dbDir +
			" --sslOnNormalPorts" +
			" --sslPEMKeyFile " + utils.ShQuote(keyFile) +
			" --sslPEMKeyPassword ignored" +
			" --bind_ip 0.0.0.0" +
			" --port " + fmt.Sprint(port) +
			" --noprealloc" +
			" --syslog" +
			" --smallfiles" +
			" --replSet " + replicaSetName,
	}
	return conf, nil
}
