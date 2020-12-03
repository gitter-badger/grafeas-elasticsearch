package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	grafeasConfig "github.com/grafeas/grafeas/go/config"
	prpb "github.com/grafeas/grafeas/proto/v1beta1/project_go_proto"

	"github.com/Jeffail/gabs/v2"
	"github.com/brianvoe/gofakeit/v5"
	"github.com/elastic/go-elasticsearch/v7"
	"github.com/elastic/go-elasticsearch/v7/esapi"
	"github.com/grafeas/grafeas/proto/v1beta1/common_go_proto"
	"github.com/grafeas/grafeas/proto/v1beta1/grafeas_go_proto"
	pb "github.com/grafeas/grafeas/proto/v1beta1/grafeas_go_proto"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("elasticsearch storage", func() {
	var (
		elasticsearchStorage *ElasticsearchStorage
		transport            *mockEsTransport
		ctx                  context.Context
		err                  error
		expectedProjectId    string
	)

	BeforeEach(func() {
		expectedProjectId = gofakeit.LetterN(10)

		transport = &mockEsTransport{}
		mockEsClient := &elasticsearch.Client{Transport: transport, API: esapi.New(transport)}

		ctx = context.Background()

		elasticsearchStorage = NewElasticsearchStore(mockEsClient, logger)
	})

	Context("creating the elasticsearch storage provider", func() {
		var (
			err                  error
			expectedStorageType  = "elasticsearch"
			expectedProjectIndex = fmt.Sprintf("%s-%s", indexPrefix, "projects")
		)

		// BeforeEach configures the happy path for this context
		// Variables configured here may be overridden in nested BeforeEach blocks
		BeforeEach(func() {
			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusOK,
				},
				{
					StatusCode: http.StatusOK,
				},
			}
		})

		// JustBeforeEach actually invokes the system under test
		JustBeforeEach(func() {
			randomStorageConfig := grafeasConfig.StorageConfiguration("{}")
			_, err = elasticsearchStorage.ElasticsearchStorageTypeProvider(expectedStorageType, &randomStorageConfig)
		})

		It("should check if an index for projects has already been created", func() {
			Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s", expectedProjectIndex)))
			Expect(transport.receivedHttpRequests[0].Method).To(Equal(http.MethodHead))
			Expect(err).ToNot(HaveOccurred())
		})

		When("an index for projects does not exist", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].StatusCode = http.StatusNotFound
			})

			It("should create the index for projects", func() {
				Expect(transport.receivedHttpRequests).To(HaveLen(2))
				Expect(transport.receivedHttpRequests[1].URL.Path).To(Equal(fmt.Sprintf("/%s", expectedProjectIndex)))
				Expect(transport.receivedHttpRequests[1].Method).To(Equal(http.MethodPut))

				assertIndexCreateBodyHasMetadataAndStringMapping(transport.receivedHttpRequests[1].Body)
			})

			When("creating the index for projects fails", func() {
				BeforeEach(func() {
					transport.preparedHttpResponses[1].StatusCode = http.StatusInternalServerError
				})

				It("should return an error", func() {
					Expect(err).To(HaveOccurred())
				})
			})
		})

		When("an index for projects already exists", func() {
			It("should not create an index for projects", func() {
				Expect(transport.receivedHttpRequests).To(HaveLen(1))
			})
		})

		When("checking for the existence of a project index fails", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].StatusCode = http.StatusInternalServerError
			})

			It("should return an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Context("creating a new Grafeas project", func() {
		var (
			createProjectErr         error
			expectedProjectIndex     string
			expectedProject          *prpb.Project
			expectedOccurrencesIndex string
			expectedNotesIndex       string
		)

		// BeforeEach configures the happy path for this context
		// Variables configured here may be overridden in nested BeforeEach blocks
		BeforeEach(func() {
			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusOK,
					Body:       createEsSearchResponse("project"),
				},
				{
					StatusCode: http.StatusOK,
				},
				{
					StatusCode: http.StatusOK,
				},
				{
					StatusCode: http.StatusOK,
				},
			}
			expectedProjectIndex = fmt.Sprintf("%s-%s", indexPrefix, "projects")
			expectedOccurrencesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "occurrences")
			expectedNotesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "notes")
		})

		// JustBeforeEach actually invokes the system under test
		JustBeforeEach(func() {
			expectedProject, createProjectErr = elasticsearchStorage.CreateProject(context.Background(), expectedProjectId, &prpb.Project{})
		})

		It("should check if the project document already exists", func() {
			Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/_search", expectedProjectIndex)))
			Expect(transport.receivedHttpRequests[0].Method).To(Equal(http.MethodGet))

			assertJsonHasValues(transport.receivedHttpRequests[0].Body, map[string]interface{}{
				"query.term.name": fmt.Sprintf("projects/%s", expectedProjectId),
			})
		})

		When("the project already exists", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0] = &http.Response{
					StatusCode: http.StatusOK,
					Body:       createEsSearchResponse("project", gofakeit.LetterN(10)),
				}
			})

			It("should return an error", func() {
				assertErrorHasGrpcStatusCode(createProjectErr, codes.AlreadyExists)
				Expect(expectedProject).To(BeNil())
			})

			It("should not create any documents or indices for the project", func() {
				Expect(transport.receivedHttpRequests).To(HaveLen(1))
			})
		})

		When("checking if the project exists returns an error", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0] = &http.Response{StatusCode: http.StatusBadRequest}
			})

			It("should return an error", func() {
				assertErrorHasGrpcStatusCode(createProjectErr, codes.Internal)
				Expect(expectedProject).To(BeNil())
			})

			It("should not create a document or indices", func() {
				Expect(transport.receivedHttpRequests).To(HaveLen(1))
			})
		})

		When("the project does not exist", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[1] = &http.Response{StatusCode: http.StatusCreated}
			})

			It("should create a new document for the project", func() {
				Expect(transport.receivedHttpRequests[1].URL.Path).To(Equal(fmt.Sprintf("/%s/_doc", expectedProjectIndex)))
				Expect(transport.receivedHttpRequests[1].Method).To(Equal(http.MethodPost))

				projectBody := &prpb.Project{}
				err := protojson.Unmarshal(ioReadCloserToByteSlice(transport.receivedHttpRequests[1].Body), proto.MessageV2(projectBody))
				Expect(err).ToNot(HaveOccurred())

				Expect(projectBody.Name).To(Equal(fmt.Sprintf("projects/%s", expectedProjectId)))
			})

			It("should create indices for storing occurrences/notes for the project", func() {
				Expect(transport.receivedHttpRequests[2].URL.Path).To(Equal(fmt.Sprintf("/%s", expectedOccurrencesIndex)))
				Expect(transport.receivedHttpRequests[2].Method).To(Equal(http.MethodPut))
				assertIndexCreateBodyHasMetadataAndStringMapping(transport.receivedHttpRequests[2].Body)

				Expect(transport.receivedHttpRequests[3].URL.Path).To(Equal(fmt.Sprintf("/%s", expectedNotesIndex)))
				Expect(transport.receivedHttpRequests[3].Method).To(Equal(http.MethodPut))
				assertIndexCreateBodyHasMetadataAndStringMapping(transport.receivedHttpRequests[3].Body)
			})

			It("should return the project", func() {
				Expect(expectedProject).ToNot(BeNil())
				Expect(expectedProject.Name).To(Equal(fmt.Sprintf("projects/%s", expectedProjectId)))
			})

			When("creating a new document fails", func() {
				BeforeEach(func() {
					transport.preparedHttpResponses[1] = &http.Response{StatusCode: http.StatusBadRequest}
				})

				It("should return an error", func() {
					assertErrorHasGrpcStatusCode(createProjectErr, codes.Internal)
					Expect(expectedProject).To(BeNil())
				})

				It("should not attempt to create indices", func() {
					Expect(transport.receivedHttpRequests).To(HaveLen(2))
				})
			})

			When("creating the indices fails", func() {
				BeforeEach(func() {
					transport.preparedHttpResponses[2] = &http.Response{StatusCode: http.StatusBadRequest}
				})

				It("should return an error", func() {
					assertErrorHasGrpcStatusCode(createProjectErr, codes.Internal)
					Expect(expectedProject).To(BeNil())
				})
			})
		})
	})

	Context("retrieving a Grafeas project", func() {
		var (
			getProjectErr        error
			expectedProjectIndex string
			expectedProject      *prpb.Project
		)

		BeforeEach(func() {
			expectedProjectIndex = fmt.Sprintf("%s-%s", indexPrefix, "projects")
			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusOK,
					Body: createGenericEsSearchResponse(&prpb.Project{
						Name: fmt.Sprintf("projects/%s", expectedProjectId),
					}),
				},
			}
		})

		JustBeforeEach(func() {
			expectedProject, getProjectErr = elasticsearchStorage.GetProject(ctx, expectedProjectId)
		})

		It("should query Grafeas for the specified project", func() {
			Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/_search", expectedProjectIndex)))
			Expect(transport.receivedHttpRequests[0].Method).To(Equal(http.MethodGet))

			assertJsonHasValues(transport.receivedHttpRequests[0].Body, map[string]interface{}{
				"query.term.name": fmt.Sprintf("projects/%s", expectedProjectId),
			})
		})

		When("elasticsearch successfully returns a project document", func() {
			It("should return the Grafeas project", func() {
				Expect(expectedProject).ToNot(BeNil())
				Expect(expectedProject.Name).To(Equal(fmt.Sprintf("projects/%s", expectedProjectId)))
			})

			It("should return without an error", func() {
				Expect(getProjectErr).ToNot(HaveOccurred())
			})
		})

		When("elasticsearch can not find the specified project document", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusOK,
						Body:       createEsSearchResponse("project"),
					},
				}
			})

			It("should return an error", func() {
				assertErrorHasGrpcStatusCode(getProjectErr, codes.NotFound)
			})
		})

		When("elasticsearch returns a bad object", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusOK,
						Body:       ioutil.NopCloser(strings.NewReader("bad object")),
					},
				}
			})

			It("should return an error", func() {
				Expect(getProjectErr).To(HaveOccurred())
			})
		})

		When("returns an unexpected response", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusBadRequest,
					},
				}
			})

			It("should return an error", func() {
				Expect(getProjectErr).To(HaveOccurred())
			})
		})
	})

	Context("deleting a Grafeas project", func() {
		var (
			deleteProjectErr         error
			expectedProjectIndex     string
			expectedNotesIndex       string
			expectedOccurrencesIndex string
		)

		BeforeEach(func() {
			expectedProjectIndex = fmt.Sprintf("%s-%s", indexPrefix, "projects")
			expectedOccurrencesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "occurrences")
			expectedNotesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "notes")

			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusOK,
					Body: structToJsonBody(&esDeleteResponse{
						Deleted: 1,
					}),
				},
				{
					StatusCode: http.StatusOK,
				},
			}
		})

		JustBeforeEach(func() {
			deleteProjectErr = elasticsearchStorage.DeleteProject(ctx, expectedProjectId)
		})

		It("should have sent the correct HTTP request", func() {
			Expect(transport.receivedHttpRequests[0].Method).To(Equal(http.MethodPost))
			Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/_delete_by_query", expectedProjectIndex)))

			assertJsonHasValues(transport.receivedHttpRequests[0].Body, map[string]interface{}{
				"query.term.name": fmt.Sprintf("projects/%s", expectedProjectId),
			})
		})

		When("elasticsearch successfully deletes the project document", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].Body = structToJsonBody(&esDeleteResponse{
					Deleted: 1,
				})
			})

			It("should attempt to delete the indices for notes / occurrences", func() {
				Expect(transport.receivedHttpRequests[1].Method).To(Equal(http.MethodDelete))
				Expect(transport.receivedHttpRequests[1].URL.Path).To(Equal(fmt.Sprintf("/%s,%s", expectedOccurrencesIndex, expectedNotesIndex)))
			})

			When("elasticsearch successfully deletes the indices for notes / occurrences", func() {
				It("should not return an error", func() {
					Expect(deleteProjectErr).ToNot(HaveOccurred())
				})
			})

			When("elasticsearch fails to delete the indices for notes / occurrences", func() {
				BeforeEach(func() {
					transport.preparedHttpResponses[1].StatusCode = http.StatusInternalServerError
				})

				It("should return an error", func() {
					assertErrorHasGrpcStatusCode(deleteProjectErr, codes.Internal)
				})
			})
		})

		When("project does not exist", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].Body = structToJsonBody(&esDeleteResponse{
					Deleted: 0,
				})
			})

			It("should return an error", func() {
				Expect(deleteProjectErr).To(HaveOccurred())
			})

			It("should not attempt to delete the indices for notes / occurrences", func() {
				Expect(transport.receivedHttpRequests).To(HaveLen(1))
			})
		})

		When("deleting the project fails", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].StatusCode = http.StatusInternalServerError
			})

			It("should return an error", func() {
				Expect(deleteProjectErr).To(HaveOccurred())
			})
		})
	})

	Context("retrieving a Grafeas occurrence", func() {
		When("elasticsearch successfully returns a occurrence document", func() {
			var (
				objectID           string
				expectedOccurrence *pb.Occurrence
			)

			BeforeEach(func() {
				objectID = gofakeit.LetterN(8)

				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusOK,
						Body:       createEsSearchResponse("occurrence", objectID),
					},
				}

				expectedOccurrence, err = elasticsearchStorage.GetOccurrence(ctx, expectedProjectId, objectID)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should have sent the correct HTTP request", func() {
				Expect(transport.receivedHttpRequests[0].Method).To(Equal("GET"))
				Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/_search", expectedProjectId)))

				requestBody, err := ioutil.ReadAll(transport.receivedHttpRequests[0].Body)
				Expect(err).ToNot(HaveOccurred())

				parsed, err := gabs.ParseJSON(requestBody)
				Expect(err).ToNot(HaveOccurred())

				Expect(parsed.Path("query.match.name").Data().(string)).To(BeEquivalentTo(fmt.Sprintf("projects/%s/occurrences/%s", expectedProjectId, objectID)))
			})

			It("should return a Grafeas occurrence", func() {
				Expect(expectedOccurrence.Name).To(Equal(objectID))
			})
		})

		When("elasticsearch can not find elasticsearch document", func() {
			var (
				objectID string
			)

			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusInternalServerError,
					},
				}
				_, err = elasticsearchStorage.GetOccurrence(ctx, expectedProjectId, objectID)
			})

			It("should return an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		When("elasticsearch returns a bad object", func() {
			var (
				objectID string
			)
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusOK,
						Body:       ioutil.NopCloser(strings.NewReader("bad object")),
					},
				}

				_, err = elasticsearchStorage.GetOccurrence(ctx, expectedProjectId, objectID)
			})

			It("should fail to decode response and return an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		When("elasticsearch returns a unexpected response", func() {
			var (
				objectID string
			)
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusNotFound,
					},
				}

				_, err = elasticsearchStorage.GetOccurrence(ctx, expectedProjectId, objectID)
			})

			It("should return an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Context("creating a new Grafeas occurrence", func() {
		var (
			actualOccurrence         *pb.Occurrence
			expectedOccurrence       *pb.Occurrence
			expectedOccurrenceId     string
			expectedOccurrencesIndex string
			actualErr                error
		)

		// BeforeEach configures the happy path for this context
		// Variables configured here may be overridden in nested BeforeEach blocks
		BeforeEach(func() {
			expectedOccurrenceId = gofakeit.LetterN(10)
			expectedOccurrencesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "occurrences")
			expectedOccurrence = generateTestOccurrence("")

			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusCreated,
					Body: structToJsonBody(&esIndexDocResponse{
						Id: expectedOccurrenceId,
					}),
				},
			}
		})

		// JustBeforeEach actually invokes the system under test
		JustBeforeEach(func() {
			occurrence := deepCopyOccurrence(expectedOccurrence)

			transport.preparedHttpResponses[0].Body = structToJsonBody(&esIndexDocResponse{
				Id: expectedOccurrenceId,
			})
			actualOccurrence, actualErr = elasticsearchStorage.CreateOccurrence(context.Background(), expectedProjectId, "", occurrence)
		})

		It("should attempt to index the occurrence as a document", func() {
			Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/_doc", expectedOccurrencesIndex)))
		})

		When("indexing the document fails", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0] = &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body: structToJsonBody(&esIndexDocResponse{
						Error: &esIndexDocError{
							Type:   gofakeit.LetterN(10),
							Reason: gofakeit.LetterN(10),
						},
					}),
				}
			})

			It("should return an error", func() {
				Expect(actualOccurrence).To(BeNil())
				Expect(actualErr).To(HaveOccurred())
			})
		})

		When("indexing the document succeeds", func() {
			It("should return the occurrence that was created", func() {
				Expect(actualErr).ToNot(HaveOccurred())

				expectedOccurrence.Name = fmt.Sprintf("projects/%s/occurrences/%s", expectedProjectId, expectedOccurrenceId)
				Expect(actualOccurrence).To(Equal(expectedOccurrence))
			})
		})
	})

	Context("creating a batch of Grafeas occurrences", func() {
		var (
			expectedErrs             []error
			actualErrs               []error
			actualOccurrences        []*pb.Occurrence
			expectedOccurrences      []*pb.Occurrence
			expectedOccurrencesIndex string
		)

		// BeforeEach configures the happy path for this context
		// Variables configured here may be overridden in nested BeforeEach blocks
		BeforeEach(func() {
			expectedOccurrencesIndex = fmt.Sprintf("%s-%s-%s", indexPrefix, expectedProjectId, "occurrences")
			expectedOccurrences = generateTestOccurrences(gofakeit.Number(2, 5))
			for i := 0; i < len(expectedOccurrences); i++ {
				expectedErrs = append(expectedErrs, nil)
			}

			transport.preparedHttpResponses = []*http.Response{
				{
					StatusCode: http.StatusOK,
					Body:       createEsBulkOccurrenceIndexResponse(expectedOccurrences, expectedErrs),
				},
			}
		})

		// JustBeforeEach actually invokes the system under test
		JustBeforeEach(func() {
			occurrences := deepCopyOccurrences(expectedOccurrences)

			transport.preparedHttpResponses[0].Body = createEsBulkOccurrenceIndexResponse(occurrences, expectedErrs)
			actualOccurrences, actualErrs = elasticsearchStorage.BatchCreateOccurrences(context.Background(), expectedProjectId, "", occurrences)
		})

		// this test parses the ndjson request body and ensures that it was formatted correctly
		It("should send a bulk request to ES to index each occurrence", func() {
			var expectedPayloads []interface{}

			for i := 0; i < len(expectedOccurrences); i++ {
				expectedPayloads = append(expectedPayloads, &esBulkQueryFragment{}, &pb.Occurrence{})
			}

			parseEsBulkIndexRequest(transport.receivedHttpRequests[0].Body, expectedPayloads)

			for i, payload := range expectedPayloads {
				if i%2 == 0 { // index metadata
					metadata := payload.(*esBulkQueryFragment)
					Expect(metadata.Index.Index).To(Equal(expectedOccurrencesIndex))
				} else { // occurrence
					occurrence := payload.(*pb.Occurrence)
					Expect(occurrence).To(Equal(expectedOccurrences[(i-1)/2]))
				}
			}
		})

		When("the bulk request returns no errors", func() {
			It("should return all created occurrences", func() {
				for i, occ := range expectedOccurrences {
					expectedOccurrence := deepCopyOccurrence(occ)
					expectedOccurrence.Name = actualOccurrences[i].Name
					Expect(actualOccurrences[i]).To(Equal(expectedOccurrence))
				}

				Expect(actualErrs).To(HaveLen(0))
			})
		})

		When("the bulk request completely fails", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses[0].StatusCode = http.StatusInternalServerError
			})

			It("should return a single error and no occurrences", func() {
				Expect(actualOccurrences).To(BeNil())
				Expect(actualErrs).To(HaveLen(1))
				Expect(actualErrs[0]).To(HaveOccurred())
			})
		})

		When("the bulk request returns some errors", func() {
			var randomErrorIndex int

			BeforeEach(func() {
				randomErrorIndex = gofakeit.Number(0, len(expectedOccurrences)-1)
				expectedErrs = []error{}
				for i := 0; i < len(expectedOccurrences); i++ {
					if i == randomErrorIndex {
						expectedErrs = append(expectedErrs, errors.New(""))
					} else {
						expectedErrs = append(expectedErrs, nil)
					}
				}
			})

			It("should only return the occurrences that were successfully created", func() {
				// remove the bad occurrence from expectedOccurrences
				copy(expectedOccurrences[randomErrorIndex:], expectedOccurrences[randomErrorIndex+1:])
				expectedOccurrences[len(expectedOccurrences)-1] = nil
				expectedOccurrences = expectedOccurrences[:len(expectedOccurrences)-1]

				// assert expectedOccurrences matches actualOccurrences
				for i, occ := range expectedOccurrences {
					expectedOccurrence := deepCopyOccurrence(occ)
					expectedOccurrence.Name = actualOccurrences[i].Name
					Expect(actualOccurrences[i]).To(Equal(expectedOccurrence))
				}

				// assert that we got a single error back
				Expect(actualErrs).To(HaveLen(1))
				Expect(actualErrs[0]).To(HaveOccurred())
			})
		})
	})

	Context("deleting a Grafeas occurrence", func() {
		var (
			objectID string
			err      error
		)

		BeforeEach(func() {
			objectID = gofakeit.LetterN(8)
		})

		When("elasticsearch successfully deletes the document", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusOK,
					},
				}

				err = elasticsearchStorage.DeleteOccurrence(ctx, expectedProjectId, "")
				Expect(err).ToNot(HaveOccurred())
			})

			It("should have sent the correct HTTP request", func() {
				Expect(transport.receivedHttpRequests[0].Method).To(Equal("POST"))
				Expect(transport.receivedHttpRequests[0].URL.Path).To(Equal(fmt.Sprintf("/%s/%s", expectedProjectId, "_delete_by_query")))
			})

		})

		When("elasticsearch fails to delete documents", func() {
			BeforeEach(func() {
				transport.preparedHttpResponses = []*http.Response{
					{
						StatusCode: http.StatusInternalServerError,
					},
				}

				err = elasticsearchStorage.DeleteOccurrence(ctx, expectedProjectId, objectID)
			})

			It("should return an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})
	})

})

func createGenericEsSearchResponse(messages ...proto.Message) io.ReadCloser {
	var hits []*esSearchResponseHit

	for _, m := range messages {
		raw, err := protojson.Marshal(proto.MessageV2(m))
		Expect(err).ToNot(HaveOccurred())

		hits = append(hits, &esSearchResponseHit{
			Source: raw,
		})
	}

	response := &esSearchResponse{
		Took: gofakeit.Number(1, 10),
		Hits: &esSearchResponseHits{
			Total: &esSearchResponseTotal{
				Value: len(hits),
			},
			Hits: hits,
		},
	}
	responseBody, err := json.Marshal(response)
	Expect(err).ToNot(HaveOccurred())

	return ioutil.NopCloser(bytes.NewReader(responseBody))
}

func createEsSearchResponse(objectType string, hitNames ...string) io.ReadCloser {
	var occurrenceHits []*esSearchResponseHit

	for _, hit := range hitNames {
		switch objectType {
		case "project":
			rawGrafeasObject, err := json.Marshal(generateTestProject(hit))
			Expect(err).ToNot(HaveOccurred())
			occurrenceHits = append(occurrenceHits, &esSearchResponseHit{
				Source: rawGrafeasObject,
			})
		case "occurrence":
			rawGrafeasObject, err := json.Marshal(generateTestOccurrence(hit))
			Expect(err).ToNot(HaveOccurred())
			occurrenceHits = append(occurrenceHits, &esSearchResponseHit{
				Source: rawGrafeasObject,
			})
		case "note":
			rawGrafeasObject, err := json.Marshal(generateTestNote(hit))
			Expect(err).ToNot(HaveOccurred())
			occurrenceHits = append(occurrenceHits, &esSearchResponseHit{
				Source: rawGrafeasObject,
			})
		}
	}

	response := &esSearchResponse{
		Took: gofakeit.Number(1, 10),
		Hits: &esSearchResponseHits{
			Total: &esSearchResponseTotal{
				Value: len(hitNames),
			},
			Hits: occurrenceHits,
		},
	}
	responseBody, err := json.Marshal(response)
	Expect(err).ToNot(HaveOccurred())

	return ioutil.NopCloser(bytes.NewReader(responseBody))
}

func createEsBulkOccurrenceIndexResponse(occurrences []*pb.Occurrence, errs []error) io.ReadCloser {
	var (
		responseItems     []*esBulkResponseItem
		responseHasErrors = false
	)
	for i := range occurrences {
		var (
			responseErr  *esIndexDocError
			responseCode = http.StatusCreated
		)
		if errs[i] != nil {
			responseErr = &esIndexDocError{
				Type:   gofakeit.LetterN(10),
				Reason: gofakeit.LetterN(10),
			}
			responseCode = http.StatusInternalServerError
			responseHasErrors = true
		}

		responseItems = append(responseItems, &esBulkResponseItem{
			Index: &esIndexDocResponse{
				Id:     gofakeit.LetterN(10),
				Status: responseCode,
				Error:  responseErr,
			},
		})
	}

	response := &esBulkResponse{
		Items:  responseItems,
		Errors: responseHasErrors,
	}

	responseBody, err := json.Marshal(response)
	Expect(err).ToNot(HaveOccurred())

	return ioutil.NopCloser(bytes.NewReader(responseBody))
}

func generateTestProject(name string) *prpb.Project {
	return &prpb.Project{
		Name: fmt.Sprintf("projects/%s", name),
	}
}

func generateTestOccurrence(name string) *pb.Occurrence {
	return &pb.Occurrence{
		Name: name,
		Resource: &grafeas_go_proto.Resource{
			Uri: gofakeit.LetterN(10),
		},
		NoteName:    gofakeit.LetterN(10),
		Kind:        common_go_proto.NoteKind_NOTE_KIND_UNSPECIFIED,
		Remediation: gofakeit.LetterN(10),
		Details:     nil,
		CreateTime:  ptypes.TimestampNow(),
	}
}

func generateTestOccurrences(l int) []*pb.Occurrence {
	var result []*pb.Occurrence
	for i := 0; i < l; i++ {
		result = append(result, generateTestOccurrence(""))
	}

	return result
}

func generateTestNote(name string) *pb.Note {
	return &pb.Note{
		Name: name,
		Kind: common_go_proto.NoteKind_NOTE_KIND_UNSPECIFIED,
	}
}

func formatJson(json string, args ...interface{}) io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(fmt.Sprintf(json, args...)))
}

func structToJsonBody(i interface{}) io.ReadCloser {
	b, err := json.Marshal(i)
	Expect(err).ToNot(HaveOccurred())

	return ioutil.NopCloser(strings.NewReader(string(b)))
}

func assertJsonHasValues(body io.ReadCloser, values map[string]interface{}) {
	requestBody, err := ioutil.ReadAll(body)
	Expect(err).ToNot(HaveOccurred())

	parsed, err := gabs.ParseJSON(requestBody)
	Expect(err).ToNot(HaveOccurred())

	for k, v := range values {
		Expect(parsed.ExistsP(k)).To(BeTrue(), "expected jsonpath %s to exist", k)

		switch v.(type) {
		case string:
			Expect(parsed.Path(k).Data().(string)).To(Equal(v.(string)))
		case bool:
			Expect(parsed.Path(k).Data().(bool)).To(Equal(v.(bool)))
		case int:
			Expect(parsed.Path(k).Data().(int)).To(Equal(v.(int)))
		default:
			Fail("assertJsonHasValues encountered unexpected type")
		}
	}
}

func assertIndexCreateBodyHasMetadataAndStringMapping(body io.ReadCloser) {
	assertJsonHasValues(body, map[string]interface{}{
		"mappings._meta.type": "grafeas",
		"mappings.dynamic_templates.0.strings.match_mapping_type": "string",
		"mappings.dynamic_templates.0.strings.mapping.type":       "keyword",
		"mappings.dynamic_templates.0.strings.mapping.norms":      false,
	})
}

func assertErrorHasGrpcStatusCode(err error, code codes.Code) {
	Expect(err).To(HaveOccurred())
	s, ok := status.FromError(err)

	Expect(ok).To(BeTrue(), "expected error to have been produced from the grpc/status package")
	Expect(s.Code()).To(Equal(code))
}

// parseEsBulkIndexRequest parses a request body in ndjson format
// each line of the body is assumed to be properly formatted JSON
// every odd line is assumed to be a regular JSON structure that can be unmarshalled via json.Unmarshal
// every even line is assumed to be a JSON structure representing a protobuf message, and will be unmarshalled using protojson.Unmarshal
func parseEsBulkIndexRequest(body io.ReadCloser, structs []interface{}) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(body)
	Expect(err).ToNot(HaveOccurred())

	requestPayload := strings.TrimSuffix(buf.String(), "\n") // _bulk requests need to end in a newline
	jsonPayloads := strings.Split(requestPayload, "\n")
	Expect(jsonPayloads).To(HaveLen(len(structs)))

	for i, s := range structs {
		if i%2 == 0 { // regular JSON
			err = json.Unmarshal([]byte(jsonPayloads[i]), s)
		} else { // protobuf JSON
			err = protojson.Unmarshal([]byte(jsonPayloads[i]), proto.MessageV2(s))
		}

		Expect(err).ToNot(HaveOccurred())
	}
}

func deepCopyOccurrences(occs []*pb.Occurrence) []*pb.Occurrence {
	var result []*pb.Occurrence
	for _, occ := range occs {
		result = append(result, deepCopyOccurrence(occ))
	}

	return result
}

func deepCopyOccurrence(occ *pb.Occurrence) *pb.Occurrence {
	result := &pb.Occurrence{}

	str, err := protojson.Marshal(proto.MessageV2(occ))
	Expect(err).ToNot(HaveOccurred())

	err = protojson.Unmarshal(str, proto.MessageV2(result))
	Expect(err).ToNot(HaveOccurred())

	return result
}

func ioReadCloserToByteSlice(r io.ReadCloser) []byte {
	builder := new(strings.Builder)
	_, err := io.Copy(builder, r)
	Expect(err).ToNot(HaveOccurred())
	return []byte(builder.String())
}