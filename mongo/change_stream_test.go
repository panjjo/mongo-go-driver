// Copyright (C) MongoDB, Inc. 2017-present.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package mongo

import (
	"context"
	"os"
	"testing"

	"mongo-go-driver/bson"
	"mongo-go-driver/bson/primitive"
	"mongo-go-driver/internal/testutil/helpers"
	"mongo-go-driver/mongo/writeconcern"
	"mongo-go-driver/x/bsonx"
	"mongo-go-driver/x/mongo/driver"
	"mongo-go-driver/x/network/command"
	"github.com/stretchr/testify/require"
)

var collectionStartingDoc = bsonx.Doc{
	{"y", bsonx.Int32(1)},
}

var doc1 = bsonx.Doc{
	{"x", bsonx.Int32(1)},
}

var wcMajority = writeconcern.New(writeconcern.WMajority())

type errorCursor struct {
	errCode int32
}

func (er *errorCursor) ID() int64 {
	return 1
}

func (er *errorCursor) Next(ctx context.Context) bool {
	return false
}

func (er *errorCursor) Decode(interface{}) error {
	return nil
}

func (er *errorCursor) DecodeBytes() (bson.Raw, error) {
	return nil, nil
}

func (er *errorCursor) Err() error {
	return command.Error{
		Code: er.errCode,
	}
}

func (er *errorCursor) Close(ctx context.Context) error {
	return nil
}

func skipIfBelow36(t *testing.T) {
	serverVersion, err := getServerVersion(createTestDatabase(t, nil))
	require.NoError(t, err)

	if compareVersions(t, serverVersion, "3.6") < 0 {
		t.Skip()
	}
}

func createStream(t *testing.T, client *Client, dbName string, collName string, pipeline interface{}) (*Collection, Cursor) {
	client.writeConcern = wcMajority
	db := client.Database(dbName)
	err := db.Drop(ctx)
	testhelpers.RequireNil(t, err, "error dropping db: %s", err)

	coll := db.Collection(collName)
	coll.writeConcern = wcMajority
	_, err = coll.InsertOne(ctx, collectionStartingDoc) // create collection on server for 3.6

	drainChannels()
	stream, err := coll.Watch(ctx, pipeline)
	testhelpers.RequireNil(t, err, "error creating stream: %s", err)

	return coll, stream
}

func skipIfBelow32(t *testing.T) {
	serverVersion, err := getServerVersion(createTestDatabase(t, nil))
	require.NoError(t, err)

	if compareVersions(t, serverVersion, "3.2") < 0 {
		t.Skip()
	}
}

func createCollectionStream(t *testing.T, dbName string, collName string, pipeline interface{}) (*Collection, Cursor) {
	client := createTestClient(t)
	return createStream(t, client, dbName, collName, pipeline)
}

func createMonitoredStream(t *testing.T, dbName string, collName string, pipeline interface{}) (*Collection, Cursor) {
	client := createMonitoredClient(t, monitor)
	return createStream(t, client, dbName, collName, pipeline)
}

func compareOptions(t *testing.T, expected bsonx.Doc, actual bsonx.Doc) {
	for _, elem := range expected {
		if elem.Key == "resumeAfter" {
			continue
		}

		var aVal bsonx.Val
		var err error

		if aVal, err = actual.LookupErr(elem.Key); err != nil {
			t.Fatalf("key %s not found in options document", elem.Key)
		}

		if !compareValues(elem.Value, aVal) {
			t.Fatalf("values for key %s do not match", elem.Key)
		}
	}
}

func comparePipelines(t *testing.T, expected bsonx.Arr, actual bsonx.Arr) {
	if len(expected) != len(actual) {
		t.Fatalf("pipeline length mismatch. expected %d got %d", len(expected), len(actual))
	}

	firstIteration := true
	for i, eVal := range expected {
		aVal := actual[i]

		if firstIteration {
			// found $changStream document with options --> must compare options, ignoring extra resume token
			compareOptions(t, eVal.Document(), aVal.Document())

			firstIteration = false
			continue
		}

		if !compareValues(eVal, aVal) {
			t.Fatalf("pipelines do not match")
		}
	}
}

func TestChangeStream(t *testing.T) {
	skipIfBelow36(t)

	t.Run("TestFirstStage", func(t *testing.T) {
		t.Parallel()

		if testing.Short() {
			t.Skip()
		}
		skipIfBelow36(t)

		if os.Getenv("TOPOLOGY") != "replica_set" {
			t.Skip()
		}

		coll := createTestCollection(t, nil, nil)

		// Ensure the database is created.
		_, err := coll.InsertOne(context.Background(), bsonx.Doc{{"x", bsonx.Int32(1)}})
		require.NoError(t, err)

		changes, err := coll.Watch(context.Background(), nil)
		require.NoError(t, err)
		defer changes.Close(ctx)

		require.NotEqual(t, len(changes.(*changeStream).pipeline), 0)

		elem := changes.(*changeStream).pipeline[0]

		doc := elem.Document()
		require.Equal(t, 1, len(doc))

		_, err = doc.LookupErr("$changeStream")
		require.NoError(t, err)
	})

	t.Run("TestReplaceRoot", func(t *testing.T) {
		t.Parallel()

		if testing.Short() {
			t.Skip()
		}
		skipIfBelow36(t)

		if os.Getenv("TOPOLOGY") != "replica_set" {
			t.Skip()
		}

		coll := createTestCollection(t, nil, nil)

		// Ensure the database is created.
		_, err := coll.InsertOne(context.Background(), bsonx.Doc{{"x", bsonx.Int32(7)}})
		require.NoError(t, err)

		pipeline := make(bsonx.Arr, 0)
		pipeline = append(pipeline,
			bsonx.Document(bsonx.Doc{{"$replaceRoot",
				bsonx.Document(bsonx.Doc{{"newRoot",
					bsonx.Document(bsonx.Doc{{"_id", bsonx.ObjectID(primitive.NewObjectID())}, {"x", bsonx.Int32(1)}})}}),
			}}))
		changes, err := coll.Watch(context.Background(), pipeline)
		require.NoError(t, err)
		defer changes.Close(ctx)

		_, err = coll.InsertOne(context.Background(), bsonx.Doc{{"x", bsonx.Int32(4)}})
		require.NoError(t, err)

		changes.Next(ctx)
		var doc *bsonx.Doc

		//Ensure the cursor returns an error when the resume token is changed.
		err = changes.Decode(&doc)
		require.Equal(t, err, ErrMissingResumeToken)
	})

	t.Run("TestNoCustomStandaloneError", func(t *testing.T) {
		t.Parallel()

		if testing.Short() {
			t.Skip()
		}
		skipIfBelow36(t)

		topology := os.Getenv("TOPOLOGY")
		if topology == "replica_set" || topology == "sharded_cluster" {
			t.Skip()
		}

		coll := createTestCollection(t, nil, nil)

		// Ensure the database is created.
		_, err := coll.InsertOne(context.Background(), bsonx.Doc{{"x", bsonx.Int32(1)}})
		require.NoError(t, err)

		_, err = coll.Watch(context.Background(), nil)
		require.Error(t, err)
		if _, ok := err.(command.Error); !ok {
			t.Errorf("Should have returned command error, but got %T", err)
		}
	})

	t.Run("TestNilCursor", func(t *testing.T) {
		cs := &changeStream{}

		if id := cs.ID(); id != 0 {
			t.Fatalf("Wrong ID returned. Expected 0 got %d", id)
		}
		if cs.Next(ctx) {
			t.Fatalf("Next returned true, expected false")
		}
		if err := cs.Decode(nil); err != ErrNilCursor {
			t.Fatalf("Wrong decode err. Expected ErrNilCursor got %s", err)
		}
		if _, err := cs.DecodeBytes(); err != ErrNilCursor {
			t.Fatalf("Wrong decode bytes err. Expected ErrNilCursor got %s", err)
		}
		if err := cs.Err(); err != nil {
			t.Fatalf("Wrong Err error. Expected nil got %s", err)
		}
		if err := cs.Close(ctx); err != nil {
			t.Fatalf("Wrong Close error. Expected nil got %s", err)
		}
	})
}

func TestChangeStream_ReplicaSet(t *testing.T) {
	skipIfBelow36(t)
	if os.Getenv("TOPOLOGY") != "replica_set" {
		t.Skip()
	}

	t.Run("TestTrackResumeToken", func(t *testing.T) {
		// Stream must continuously track last seen resumeToken

		coll, stream := createCollectionStream(t, "TrackTokenDB", "TrackTokenColl", bsonx.Doc{})
		defer closeCursor(stream)

		cs := stream.(*changeStream)
		if cs.resumeToken != nil {
			t.Fatalf("non-nil error on stream")
		}

		coll.writeConcern = wcMajority
		_, err := coll.InsertOne(ctx, doc1)
		testhelpers.RequireNil(t, err, "error running insertOne: %s", err)
		if !stream.Next(ctx) {
			t.Fatalf("no change found")
		}

		_, err = stream.DecodeBytes()
		testhelpers.RequireNil(t, err, "error decoding bytes: %s", err)

		testhelpers.RequireNotNil(t, cs.resumeToken, "no resume token found after first change")
	})

	t.Run("TestMissingResumeToken", func(t *testing.T) {
		// Stream will throw an error if the server response is missing the resume token
		idDoc := bsonx.Doc{{"_id", bsonx.Int32(0)}}
		pipeline := []bsonx.Doc{
			{
				{"$project", bsonx.Document(idDoc)},
			},
		}

		coll, stream := createCollectionStream(t, "MissingTokenDB", "MissingTokenColl", pipeline)
		defer closeCursor(stream)

		coll.writeConcern = wcMajority
		_, err := coll.InsertOne(ctx, doc1)
		testhelpers.RequireNil(t, err, "error running insertOne: %s", err)
		if !stream.Next(ctx) {
			t.Fatal("no change found")
		}

		_, err = stream.DecodeBytes()
		if err == nil || err != ErrMissingResumeToken {
			t.Fatalf("expected ErrMissingResumeToken, got %s", err)
		}
	})

	t.Run("ResumeOnce", func(t *testing.T) {
		// ChangeStream will automatically resume one time on a resumable error (including not master) with the initial
		// pipeline and options, except for the addition/update of a resumeToken.

		coll, stream := createMonitoredStream(t, "ResumeOnceDB", "ResumeOnceColl", nil)
		defer closeCursor(stream)
		startCmd := (<-startedChan).Command
		startPipeline := startCmd.Lookup("pipeline").Array()

		cs := stream.(*changeStream)

		kc := command.KillCursors{
			NS:  cs.ns,
			IDs: []int64{cs.ID()},
		}

		_, err := driver.KillCursors(ctx, kc, cs.client.topology, cs.db.writeSelector)
		testhelpers.RequireNil(t, err, "error running killCursors cmd: %s", err)

		_, err = coll.InsertOne(ctx, doc1)
		testhelpers.RequireNil(t, err, "error inserting doc: %s", err)

		drainChannels()
		stream.Next(ctx)

		//Next() should cause getMore, killCursors and aggregate to run
		if len(startedChan) != 3 {
			t.Fatalf("expected 3 events waiting, got %d", len(startedChan))
		}

		<-startedChan            // getMore
		<-startedChan            // killCursors
		started := <-startedChan // aggregate

		if started.CommandName != "aggregate" {
			t.Fatalf("command name mismatch. expected aggregate got %s", started.CommandName)
		}

		pipeline := started.Command.Lookup("pipeline").Array()

		if len(startPipeline) != len(pipeline) {
			t.Fatalf("pipeline len mismatch")
		}

		comparePipelines(t, startPipeline, pipeline)
	})

	t.Run("NoResumeForAggregateErrors", func(t *testing.T) {
		// ChangeStream will not attempt to resume on any error encountered while executing an aggregate command.
		dbName := "NoResumeDB"
		collName := "NoResumeColl"
		coll := createTestCollection(t, &dbName, &collName)

		idDoc := bsonx.Doc{{"id", bsonx.Int32(0)}}
		stream, err := coll.Watch(ctx, []*bsonx.Doc{
			{
				{"$unsupportedStage", bsonx.Document(idDoc)},
			},
		})
		testhelpers.RequireNil(t, stream, "stream was not nil")
		testhelpers.RequireNotNil(t, err, "error was nil")
	})

	t.Run("NoResumeErrors", func(t *testing.T) {
		// ChangeStream will not attempt to resume after encountering error code 11601 (Interrupted),
		// 136 (CappedPositionLost), or 237 (CursorKilled) while executing a getMore command.

		var tests = []struct {
			name    string
			errCode int32
		}{
			{"ErrorInterrupted", errorInterrupted},
			{"ErrorCappedPostionLost", errorCappedPositionLost},
			{"ErrorCursorKilled", errorCursorKilled},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, stream := createMonitoredStream(t, "ResumeOnceDB", "ResumeOnceColl", nil)
				defer closeCursor(stream)
				cs := stream.(*changeStream)
				cs.cursor = &errorCursor{
					errCode: tc.errCode,
				}

				drainChannels()
				if stream.Next(ctx) {
					t.Fatal("stream Next() returned true, expected false")
				}

				// no commands should be started because fake cursor's Next() does not call getMore
				if len(startedChan) != 0 {
					t.Fatalf("expected 1 command started, got %d", len(startedChan))
				}
			})
		}
	})

	t.Run("ServerSelection", func(t *testing.T) {
		// ChangeStream will perform server selection before attempting to resume, using initial readPreference
		t.Skip("Skipping for lack of SDAM monitoring")
	})

	t.Run("CursorNotClosed", func(t *testing.T) {
		// Ensure that a cursor returned from an aggregate command with a cursor id and an initial empty batch is not

		_, stream := createCollectionStream(t, "CursorNotClosedDB", "CursorNotClosedColl", nil)
		defer closeCursor(stream)
		cs := stream.(*changeStream)

		if cs.sess.(*sessionImpl).Client.Terminated {
			t.Fatalf("session was prematurely terminated")
		}
	})

	t.Run("NoExceptionFromKillCursors", func(t *testing.T) {
		// The killCursors command sent during the "Resume Process" must not be allowed to throw an exception

		// fail points don't work for mongos or <4.0
		if os.Getenv("TOPOLOGY") == "sharded_cluster" {
			t.Skip("skipping for sharded clusters")
		}

		version, err := getServerVersion(createTestDatabase(t, nil))
		testhelpers.RequireNil(t, err, "error getting server version: %s", err)

		if compareVersions(t, version, "4.0") < 0 {
			t.Skip("skipping for version < 4.0")
		}

		coll, stream := createMonitoredStream(t, "NoExceptionsDB", "NoExceptionsColl", nil)
		defer closeCursor(stream)
		cs := stream.(*changeStream)

		// kill cursor to force a resumable error
		kc := command.KillCursors{
			NS:  cs.ns,
			IDs: []int64{cs.ID()},
		}

		_, err = driver.KillCursors(ctx, kc, cs.client.topology, cs.db.writeSelector)
		testhelpers.RequireNil(t, err, "error running killCursors cmd: %s", err)

		adminDb := coll.client.Database("admin")
		modeDoc := bsonx.Doc{
			{"times", bsonx.Int32(1)},
		}
		dataArray := bsonx.Arr{
			bsonx.String("killCursors"),
		}
		dataDoc := bsonx.Doc{
			{"failCommands", bsonx.Array(dataArray)},
			{"errorCode", bsonx.Int32(184)},
		}

		result := adminDb.RunCommand(ctx, bsonx.Doc{
			{"configureFailPoint", bsonx.String("failCommand")},
			{"mode", bsonx.Document(modeDoc)},
			{"data", bsonx.Document(dataDoc)},
		})

		testhelpers.RequireNil(t, err, "error creating fail point: %s", result.err)

		if !stream.Next(ctx) {
			t.Fatal("stream Next() returned false, expected true")
		}
	})

	t.Run("OperationTimeIncluded", func(t *testing.T) {
		// $changeStream stage for ChangeStream against a server >=4.0 that has not received any results yet MUST
		// include a startAtOperationTime option when resuming a changestream.

		version, err := getServerVersion(createTestDatabase(t, nil))
		testhelpers.RequireNil(t, err, "error getting server version: %s", err)

		if compareVersions(t, version, "4.0") < 0 {
			t.Skip("skipping for version < 4.0")
		}

		_, stream := createMonitoredStream(t, "IncludeTimeDB", "IncludeTimeColl", nil)
		defer closeCursor(stream)
		cs := stream.(*changeStream)

		// kill cursor to force a resumable error
		kc := command.KillCursors{
			NS:  cs.ns,
			IDs: []int64{cs.ID()},
		}

		_, err = driver.KillCursors(ctx, kc, cs.client.topology, cs.db.writeSelector)
		testhelpers.RequireNil(t, err, "error running killCursors cmd: %s", err)

		drainChannels()
		stream.Next(ctx)

		// channel should have getMore, killCursors, and aggregate
		if len(startedChan) != 3 {
			t.Fatalf("expected 3 commands started, got %d", len(startedChan))
		}

		<-startedChan
		<-startedChan

		aggCmd := <-startedChan
		if aggCmd.CommandName != "aggregate" {
			t.Fatalf("command name mismatch. expected aggregate, got %s", aggCmd.CommandName)
		}

		pipeline := aggCmd.Command.Lookup("pipeline").Array()
		if len(pipeline) == 0 {
			t.Fatalf("empty pipeline")
		}
		csVal := pipeline[0] // doc with nested options document (key $changeStream)
		testhelpers.RequireNil(t, err, "pipeline is empty")

		optsVal, err := csVal.Document().LookupErr("$changeStream")
		testhelpers.RequireNil(t, err, "key $changeStream not found")

		if _, err := optsVal.Document().LookupErr("startAtOperationTime"); err != nil {
			t.Fatal("key startAtOperationTime not found in command")
		}
	})

	// There's another test: ChangeStream will resume after a killCursors command is issued for its child cursor.
	// But, killCursors was already used to cause an error for the ResumeOnce test, so this does not need to be tested
	// again.
}
