package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/discord-gophers/goapi-gen/types"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xtuser777/nlw-journey-trilha-go/internal/api/spec"
	"github.com/xtuser777/nlw-journey-trilha-go/internal/pgstore"

	"go.uber.org/zap"
)

type mailer interface {
	SendConfirmTripEmailToTripOwner(uuid.UUID) error
	SendEmailInvitations(trupID uuid.UUID) error
}

type store interface {
	GetParticipant(context.Context, uuid.UUID) (pgstore.Participant, error)
	ConfirmParticipant(context.Context, uuid.UUID) error
	CreateTrip(context.Context, *pgxpool.Pool, spec.CreateTripRequest) (uuid.UUID, error)
	GetTrip(ctx context.Context, id uuid.UUID) (pgstore.Trip, error)
	UpdateTrip(ctx context.Context, arg pgstore.UpdateTripParams) error
	GetTripActivities(ctx context.Context, tripID uuid.UUID) ([]pgstore.Activity, error)
	CreateActivity(ctx context.Context, arg pgstore.CreateActivityParams) (uuid.UUID, error)
	WithTx(tx pgx.Tx) *pgstore.Queries
	GetTripLinks(ctx context.Context, tripID uuid.UUID) ([]pgstore.Link, error)
	CreateTripLink(ctx context.Context, arg pgstore.CreateTripLinkParams) (uuid.UUID, error)
	GetParticipants(ctx context.Context, tripID uuid.UUID) ([]pgstore.Participant, error)
}

type API struct {
	store     store
	logger    *zap.Logger
	validator *validator.Validate
	pool      *pgxpool.Pool
	mailer    mailer
}

func NewApi(pool *pgxpool.Pool, logger *zap.Logger, mailer mailer) API {
	validator := validator.New(validator.WithRequiredStructEnabled())
	return API{
		pgstore.New(pool),
		logger,
		validator,
		pool,
		mailer,
	}
}

// Confirms a participant on a trip.
// (PATCH /participants/{participantId}/confirm)
func (api *API) PatchParticipantsParticipantIDConfirm(w http.ResponseWriter, r *http.Request, participantID string) *spec.Response {
	id, err := uuid.Parse(participantID)
	if err != nil {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	participant, err := api.store.GetParticipant(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
				Message: "participant not found",
			})
		}
		api.logger.Error("failed to get participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	if participant.IsConfirmed {
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "participant already confirmed",
		})
	}

	if err := api.store.ConfirmParticipant(r.Context(), id); err != nil {
		api.logger.Error("failed to confim participant", zap.Error(err), zap.String("participant_id", participantID))
		return spec.PatchParticipantsParticipantIDConfirmJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	return spec.PatchParticipantsParticipantIDConfirmJSON204Response(nil)
}

// Create a new trip
// (POST /trips)
func (api *API) PostTrips(w http.ResponseWriter, r *http.Request) *spec.Response {
	var body spec.CreateTripRequest

	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		spec.PostTripsJSON400Response(spec.Error{Message: "invalid json: " + err.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	tripID, err := api.store.CreateTrip(r.Context(), api.pool, body)
	if err != nil {
		return spec.PostTripsJSON400Response(spec.Error{Message: "failed to create trip, try again"})
	}

	go func() {
		if err := api.mailer.SendConfirmTripEmailToTripOwner(tripID); err != nil {
			api.logger.Error(
				"failed to send email on PostTrips",
				zap.Error(err),
				zap.String("trip_id", tripID.String()),
			)
		}
	}()

	return spec.PostTripsJSON201Response(spec.CreateTripResponse{TripID: tripID.String()})
}

// Get a trip details.
// (GET /trips/{tripId})
func (api *API) GetTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, err := uuid.Parse(tripID)
	if err != nil {
		return spec.GetTripsTripIDJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	trip, err := api.store.GetTrip(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	responseTrip := spec.GetTripDetailsResponseTripObj{
		ID:          trip.ID.String(),
		Destination: trip.Destination,
		StartsAt:    trip.StartsAt.Time,
		EndsAt:      trip.EndsAt.Time,
	}

	return spec.GetTripsTripIDJSON200Response(spec.GetTripDetailsResponse{Trip: responseTrip})
}

// Update a trip.
// (PUT /trips/{tripId})
func (api *API) PutTripsTripID(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, err := uuid.Parse(tripID)
	if err != nil {
		return spec.PutTripsTripIDJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	trip, err := api.store.GetTrip(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PutTripsTripIDJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PutTripsTripIDJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	var body spec.PutTripsTripIDJSONRequestBody

	errJson := json.NewDecoder(r.Body).Decode(&body)
	if errJson != nil {
		spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid json: " + errJson.Error()})
	}

	if err := api.validator.Struct(body); err != nil {
		return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "invalid input: " + err.Error()})
	}

	params := pgstore.UpdateTripParams{
		ID:          trip.ID,
		Destination: body.Destination,
		IsConfirmed: trip.IsConfirmed,
		StartsAt:    pgtype.Timestamp{Valid: true, Time: body.StartsAt},
		EndsAt:      pgtype.Timestamp{Valid: true, Time: body.EndsAt},
	}

	errExec := api.store.UpdateTrip(r.Context(), params)
	if errExec != nil {
		return spec.PutTripsTripIDJSON400Response(spec.Error{Message: "failed to update trip, try again"})
	}

	return spec.PutTripsTripIDJSON204Response(body)
}

// Get a trip activities.
// (GET /trips/{tripId}/activities)
func (api *API) GetTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, err := uuid.Parse(tripID)
	if err != nil {
		return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	acts, err := api.store.GetTripActivities(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDActivitiesJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	var responseActs []spec.GetTripActivitiesResponseInnerArray

	for i := 0; i < len(acts); i++ {
		responseActs = append(responseActs, spec.GetTripActivitiesResponseInnerArray{
			ID:       acts[i].ID.String(),
			Title:    acts[i].Title,
			OccursAt: acts[i].OccursAt.Time,
		})
	}

	var responseActsDates []time.Time

	for j := 0; j < len(responseActs); j++ {
		if j == 0 {
			responseActsDates = append(responseActsDates, responseActs[j].OccursAt)
		} else {
			if responseActsDates[j-1] != responseActs[j].OccursAt {
				responseActsDates = append(responseActsDates, responseActs[j].OccursAt)
			}
		}
	}

	var responseActsFinal []spec.GetTripActivitiesResponseOuterArray

	if len(responseActsDates) == 0 {
		responseActsFinal = []spec.GetTripActivitiesResponseOuterArray{}
	}

	for x := 0; x < len(responseActsDates); x++ {
		var actsInner []spec.GetTripActivitiesResponseInnerArray
		for y := 0; y < len(responseActs); y++ {
			if responseActs[y].OccursAt == responseActsDates[x] {
				actsInner = append(actsInner, responseActs[y])
			}
		}
		responseActsFinal = append(
			responseActsFinal,
			spec.GetTripActivitiesResponseOuterArray{
				Activities: actsInner,
				Date:       responseActsDates[x],
			},
		)
	}

	return spec.GetTripsTripIDActivitiesJSON200Response(spec.GetTripActivitiesResponse{responseActsFinal})
}

// Create a trip activity.
// (POST /trips/{tripId}/activities)
func (api *API) PostTripsTripIDActivities(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, err := api.store.GetTrip(r.Context(), tripUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(err), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	var body spec.CreateActivityRequest
	id, err := api.store.CreateActivity(r.Context(), pgstore.CreateActivityParams{
		TripID:   tripUUID,
		Title:    body.Title,
		OccursAt: pgtype.Timestamp{Valid: true, Time: body.OccursAt},
	})
	if err != nil {
		return spec.PostTripsTripIDActivitiesJSON400Response(spec.Error{Message: "failed to create activity, try again"})
	}

	return spec.PostTripsTripIDActivitiesJSON201Response(spec.CreateActivityResponse{ActivityID: id.String()})
}

// Confirm a trip and send e-mail invitations.
// (GET /trips/{tripId}/confirm)
func (api *API) GetTripsTripIDConfirm(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	tripUUID, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, errTrip := api.store.GetTrip(r.Context(), tripUUID)
	if errTrip != nil {
		if errors.Is(errTrip, pgx.ErrNoRows) {
			return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(errTrip), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	err := api.store.ConfirmParticipant(r.Context(), tripUUID)
	if err != nil {
		return spec.GetTripsTripIDConfirmJSON400Response(spec.Error{
			Message: "failed to confirm participant, try again",
		})
	}

	go func() {
		if err := api.mailer.SendEmailInvitations(tripUUID); err != nil {
			api.logger.Error(
				"failed to send email on GetTripsTripIDConfirm",
				zap.Error(err),
				zap.String("trip_id", tripUUID.String()),
			)
		}
	}()

	return spec.GetTripsTripIDConfirmJSON204Response(nil)
}

// Invite someone to the trip.
// (POST /trips/{tripId}/invites)
func (api *API) PostTripsTripIDInvites(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, errTrip := api.store.GetTrip(r.Context(), id)
	if errTrip != nil {
		if errors.Is(errTrip, pgx.ErrNoRows) {
			return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(errTrip), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	var body spec.PostTripsTripIDInvitesJSONBody

	errJson := json.NewDecoder(r.Body).Decode(&body)
	if errJson != nil {
		spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid json: " + errJson.Error()})
	}

	if errVal := api.validator.Struct(body); errVal != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{Message: "invalid input: " + errVal.Error()})
	}

	tx, errTx := api.pool.Begin(r.Context())
	if errTx != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
			Message: "pgstore: failed to begin tx for PostTripsTripIDInvites",
		})
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	qtx := api.store.WithTx(tx)

	participants := make([]pgstore.InviteParticipantsToTripParams, 1)
	participants[0] = pgstore.InviteParticipantsToTripParams{
		TripID: id,
		Email:  string(body.Email),
	}

	if _, errExe := qtx.InviteParticipantsToTrip(r.Context(), participants); errExe != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
			Message: "pgstore: failed to insert participants for PostTripsTripIDInvites",
		})
	}

	if errCom := tx.Commit(r.Context()); errCom != nil {
		return spec.PostTripsTripIDInvitesJSON400Response(spec.Error{
			Message: "pgstore: failed to commit tx for PostTripsTripIDInvites",
		})
	}

	return spec.PostTripsTripIDInvitesJSON201Response(nil)
}

// Get a trip links.
// (GET /trips/{tripId}/links)
func (api *API) GetTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, errTrip := api.store.GetTrip(r.Context(), id)
	if errTrip != nil {
		if errors.Is(errTrip, pgx.ErrNoRows) {
			return spec.GetTripsTripIDLinksJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(errTrip), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	links, errExec := api.store.GetTripLinks(r.Context(), id)
	if errExec != nil {
		return spec.GetTripsTripIDLinksJSON400Response(spec.Error{
			Message: "links for found",
		})
	}

	var responseLinks []spec.GetLinksResponseArray

	if len(links) == 0 {
		responseLinks = []spec.GetLinksResponseArray{}
	}

	for _, link := range links {
		responseLinks = append(responseLinks, spec.GetLinksResponseArray{
			ID:    link.ID.String(),
			Title: link.Title,
			URL:   link.Url,
		})
	}

	return spec.GetTripsTripIDLinksJSON200Response(spec.GetLinksResponse{Links: responseLinks})
}

// Create a trip link.
// (POST /trips/{tripId}/links)
func (api *API) PostTripsTripIDLinks(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, errTrip := api.store.GetTrip(r.Context(), id)
	if errTrip != nil {
		if errors.Is(errTrip, pgx.ErrNoRows) {
			return spec.PostTripsTripIDLinksJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(errTrip), zap.String("trip_id", tripID))
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	var body spec.PostTripsTripIDLinksJSONBody
	errJson := json.NewDecoder(r.Body).Decode(&body)
	if errJson != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid json: " + errJson.Error()})
	}

	if errVal := api.validator.Struct(body); errVal != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{Message: "invalid input: " + errVal.Error()})
	}

	uuid, err := api.store.CreateTripLink(r.Context(), pgstore.CreateTripLinkParams{
		TripID: id,
		Title:  body.Title,
		Url:    body.URL,
	})
	if err != nil {
		return spec.PostTripsTripIDLinksJSON400Response(spec.Error{
			Message: "fail to insert trip link",
		})
	}

	return spec.PostTripsTripIDLinksJSON201Response(spec.CreateLinkResponse{LinkID: uuid.String()})
}

// Get a trip participants.
// (GET /trips/{tripId}/participants)
func (api *API) GetTripsTripIDParticipants(w http.ResponseWriter, r *http.Request, tripID string) *spec.Response {
	id, errUUID := uuid.Parse(tripID)
	if errUUID != nil {
		return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{
			Message: "invalid uuid",
		})
	}

	_, errTrip := api.store.GetTrip(r.Context(), id)
	if errTrip != nil {
		if errors.Is(errTrip, pgx.ErrNoRows) {
			return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{
				Message: "trip not found",
			})
		}
		api.logger.Error("failed to get trip", zap.Error(errTrip), zap.String("trip_id", tripID))
		return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{
			Message: "something went wrong, try again",
		})
	}

	parts, err := api.store.GetParticipants(r.Context(), id)
	if err != nil {
		return spec.GetTripsTripIDParticipantsJSON400Response(spec.Error{
			Message: "fail to get trip participants",
		})
	}

	var responseParts []spec.GetTripParticipantsResponseArray
	for _, part := range parts {
		responseParts = append(responseParts, spec.GetTripParticipantsResponseArray{
			ID:          part.ID.String(),
			Email:       types.Email(part.Email),
			IsConfirmed: part.IsConfirmed,
			Name:        &part.Email,
		})
	}

	return spec.GetTripsTripIDParticipantsJSON200Response(spec.GetTripParticipantsResponse{
		Participants: responseParts,
	})
}
