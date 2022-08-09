package mongohelper

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/utils/pointer"

	api "github.com/nunnatsa/mongodb-operator/api/v1alpha1"
)

const (
	MongoDbPort = 27017
)

type member struct {
	Id   int    `bson:"_id" json:"_id"`
	Name string `bson:"name,omitempty" json:"name,omitempty"`
}
type ReplicaSetStatus struct {
	OK      *int     `json:"ok,omitempty"`
	Members []member `json:"members,omitempty"`
}

func (s ReplicaSetStatus) IsOK() bool {
	return s.OK != nil && *s.OK == 1
}

func EnsureMongoDBReplicaSet(ctx context.Context, mdb *api.MongoDB, replicas int32, logger logr.Logger) (*ReplicaSetStatus, error) {
	mcl, err := connect(ctx, mdb)
	if err != nil {
		logger.Error(err, "failed to connect to the replica set")
		return nil, err
	}

	defer func() { _ = mcl.Disconnect(ctx) }()

	status, err := getRSStatus(ctx, mcl)
	if err != nil {
		if strings.Contains(err.Error(), "(NotYetInitialized)") {
			err = initiateReplicaSet(ctx, mcl, mdb, replicas)
			if err != nil {
				logger.Error(err, "failed to initiate replica set")
				return nil, err
			}
		} else {
			logger.Error(err, "failed to get replica set status")
		}

		return nil, err
	}

	if len(status.Members) != int(replicas) {
		err = resetConfig(ctx, mcl, mdb, replicas, logger)
		if err != nil {
			return nil, err
		}
		return getRSStatus(ctx, mcl)
	}

	return status, nil
}

func connect(ctx context.Context, mdb *api.MongoDB) (*mongo.Client, error) {
	client, err := mongo.Connect(ctx, &options.ClientOptions{
		Hosts: []string{
			fmt.Sprintf("%s-0.%s.%s:27017", mdb.Name, mdb.Name, mdb.Namespace),
		},
		Direct: pointer.Bool(true),
	})

	if err != nil {
		return nil, err
	}

	return client, nil
}

func getRSStatus(ctx context.Context, client *mongo.Client) (*ReplicaSetStatus, error) {
	cmd := bson.M{"replSetGetStatus": 1}
	status := &ReplicaSetStatus{}
	err := client.Database("admin").RunCommand(ctx, cmd).Decode(status)
	if err != nil {
		return nil, err
	}

	return status, nil
}

func initiateReplicaSet(ctx context.Context, client *mongo.Client, mdb *api.MongoDB, nodes int32) error {
	config := getConfig(mdb, nodes)

	var result = bson.M{}
	err := client.Database("admin").RunCommand(ctx, bson.M{"replSetInitiate": config}).Decode(&result)
	if err != nil {
		return err
	}

	return nil
}

func resetConfig(ctx context.Context, client *mongo.Client, mdb *api.MongoDB, nodes int32, logger logr.Logger) error {
	config, err := readConfig(ctx, client)
	if err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("replica set configuration: %#v", config))

	config["members"] = getMembers(mdb, nodes)
	version := config["version"].(int32)
	version++
	config["version"] = version

	var result = bson.M{}
	err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetReconfig", Value: config}, {Key: "force", Value: true}}).Decode(&result)
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("replSetReconfig result: %#v", result))

	return nil
}

func readConfig(ctx context.Context, client *mongo.Client) (bson.M, error) {
	var result = bson.M{}
	err := client.Database("admin").RunCommand(ctx, bson.M{"replSetGetConfig": 1}).Decode(&result)
	if err != nil {
		return nil, err
	}

	return result["config"].(bson.M), nil
}

func getConfig(mdb *api.MongoDB, nodes int32) bson.M {
	members := getMembers(mdb, nodes)

	return bson.M{
		"_id":     mdb.Name,
		"members": members,
	}
}

func getMembers(mdb *api.MongoDB, nodes int32) []bson.M {
	members := make([]bson.M, nodes)
	for node := int32(0); node < nodes; node++ {
		members[node] = bson.M{"_id": node, "host": fmt.Sprintf("%s-%d.%s:27017", mdb.Name, node, mdb.Name)}
	}

	return members
}
