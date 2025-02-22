package dao

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/RedHatInsights/event-schemas-go/apps/repositories/v1"
	"github.com/content-services/content-sources-backend/pkg/api"
	ce "github.com/content-services/content-sources-backend/pkg/errors"
	"github.com/content-services/content-sources-backend/pkg/models"
	"github.com/content-services/content-sources-backend/pkg/notifications"
	"github.com/content-services/yummy/pkg/yum"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repositoryConfigDaoImpl struct {
	db      *gorm.DB
	yumRepo yum.YumRepository
}

func GetRepositoryConfigDao(db *gorm.DB) RepositoryConfigDao {
	return repositoryConfigDaoImpl{
		db:      db,
		yumRepo: &yum.Repository{},
	}
}

func DBErrorToApi(e error) *ce.DaoError {
	var dupKeyName string
	if e == nil {
		return nil
	}

	pgError, ok := e.(*pgconn.PgError)
	if ok {
		if pgError.Code == "23505" {
			switch pgError.ConstraintName {
			case "repo_and_org_id_unique":
				dupKeyName = "URL"
			case "repositories_unique_url":
				dupKeyName = "URL"
			case "name_and_org_id_unique":
				dupKeyName = "name"
			}
			return &ce.DaoError{BadValidation: true, Message: "Repository with this " + dupKeyName + " already belongs to organization"}
		}
	}
	dbError, ok := e.(models.Error)
	if ok {
		return &ce.DaoError{BadValidation: dbError.Validation, Message: dbError.Message}
	}
	return &ce.DaoError{Message: e.Error()}
}

func (r repositoryConfigDaoImpl) Create(newRepoReq api.RepositoryRequest) (api.RepositoryResponse, error) {
	var newRepo models.Repository
	var newRepoConfig models.RepositoryConfiguration
	ApiFieldsToModel(newRepoReq, &newRepoConfig, &newRepo)

	cleanedUrl := models.CleanupURL(newRepo.URL)
	if err := r.db.Where("url = ?", cleanedUrl).FirstOrCreate(&newRepo).Error; err != nil {
		return api.RepositoryResponse{}, DBErrorToApi(err)
	}

	if newRepoReq.OrgID != nil {
		newRepoConfig.OrgID = *newRepoReq.OrgID
	}
	if newRepoReq.AccountID != nil {
		newRepoConfig.AccountID = *newRepoReq.AccountID
	}
	newRepoConfig.RepositoryUUID = newRepo.Base.UUID

	if err := r.db.Create(&newRepoConfig).Error; err != nil {
		return api.RepositoryResponse{}, DBErrorToApi(err)
	}

	var created api.RepositoryResponse
	ModelToApiFields(newRepoConfig, &created)

	created.URL = newRepo.URL
	created.Status = newRepo.Status

	notifications.SendNotification(
		newRepoConfig.OrgID,
		notifications.RepositoryCreated,
		[]repositories.Repositories{notifications.MapRepositoryResponse(created)},
	)

	return created, nil
}

func (r repositoryConfigDaoImpl) BulkCreate(newRepositories []api.RepositoryRequest) ([]api.RepositoryResponse, []error) {
	var responses []api.RepositoryResponse
	var errs []error

	_ = r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		responses, errs = r.bulkCreate(tx, newRepositories)
		if len(errs) > 0 {
			err = errors.New("rollback bulk create")
		}
		return err
	})

	mappedValues := []repositories.Repositories{}
	for i := 0; i < len(responses); i++ {
		mappedValues = append(mappedValues, notifications.MapRepositoryResponse(responses[i]))
	}
	notifications.SendNotification(*newRepositories[0].OrgID, notifications.RepositoryCreated, mappedValues)

	return responses, errs
}

func (r repositoryConfigDaoImpl) bulkCreate(tx *gorm.DB, newRepositories []api.RepositoryRequest) ([]api.RepositoryResponse, []error) {
	var dbErr error
	size := len(newRepositories)
	newRepoConfigs := make([]models.RepositoryConfiguration, size)
	newRepos := make([]models.Repository, size)
	responses := make([]api.RepositoryResponse, size)
	errors := make([]error, size)

	tx.SavePoint("beforecreate")
	for i := 0; i < size; i++ {
		if newRepositories[i].OrgID != nil {
			newRepoConfigs[i].OrgID = *(newRepositories[i].OrgID)
		}
		if newRepositories[i].AccountID != nil {
			newRepoConfigs[i].AccountID = *(newRepositories[i].AccountID)
		}
		ApiFieldsToModel(newRepositories[i], &newRepoConfigs[i], &newRepos[i])
		newRepos[i].Status = "Pending"
		cleanedUrl := models.CleanupURL(newRepos[i].URL)
		create := tx.Where("url = ?", cleanedUrl).FirstOrCreate(&newRepos[i])
		if err := create.Error; err != nil {
			dbErr = DBErrorToApi(err)
			errors[i] = dbErr
			tx.RollbackTo("beforecreate")
			continue
		}

		newRepoConfigs[i].RepositoryUUID = newRepos[i].UUID
		if err := tx.Create(&newRepoConfigs[i]).Error; err != nil {
			dbErr = DBErrorToApi(err)
			errors[i] = dbErr
			tx.RollbackTo("beforecreate")
			continue
		}

		// If there is at least 1 error, skip creating responses
		if dbErr == nil {
			ModelToApiFields(newRepoConfigs[i], &responses[i])
			responses[i].URL = newRepos[i].URL
			responses[i].Status = newRepos[i].Status
		}
	}

	// If there are no errors at all, return empty error slice.
	// If there is at least 1 error, return empty response slice.
	if dbErr == nil {
		return responses, []error{}
	} else {
		return []api.RepositoryResponse{}, errors
	}
}

func (r repositoryConfigDaoImpl) List(
	OrgID string,
	pageData api.PaginationData,
	filterData api.FilterData,
) (api.RepositoryCollectionResponse, int64, error) {
	var totalRepos int64
	repoConfigs := make([]models.RepositoryConfiguration, 0)

	filteredDB := r.db

	filteredDB = filteredDB.Where("org_id = ?", OrgID).
		Joins("inner join repositories on repository_configurations.repository_uuid = repositories.uuid")

	if filterData.Name != "" {
		filteredDB = filteredDB.Where("name = ?", filterData.Name)
	}
	if filterData.URL != "" {
		filteredDB = filteredDB.Where("repositories.url = ?", models.CleanupURL(filterData.URL))
	}

	if filterData.AvailableForArch != "" {
		filteredDB = filteredDB.Where("arch = ? OR arch = '' OR arch = 'any'", filterData.AvailableForArch)
	}
	if filterData.AvailableForVersion != "" {
		filteredDB = filteredDB.
			Where("? = any (versions) OR 'any' = any (versions) OR array_length(versions, 1) IS NULL", filterData.AvailableForVersion)
	}

	if filterData.Search != "" {
		containsSearch := "%" + filterData.Search + "%"
		filteredDB = filteredDB.
			Where("name LIKE ? OR url LIKE ?", containsSearch, containsSearch)
	}

	if filterData.Arch != "" {
		arches := strings.Split(filterData.Arch, ",")
		filteredDB = filteredDB.Where("arch IN ?", arches)
	}

	if filterData.Version != "" {
		versions := strings.Split(filterData.Version, ",")
		orGroup := r.db.Where("? = any (versions)", versions[0])
		for i := 1; i < len(versions); i++ {
			orGroup = orGroup.Or("? = any (versions)", versions[i])
		}
		filteredDB = filteredDB.Where(orGroup)
	}

	if filterData.Status != "" {
		statuses := strings.Split(filterData.Status, ",")
		filteredDB = filteredDB.Where("status IN ?", statuses)
	}

	sortMap := map[string]string{
		"name":                    "name",
		"url":                     "url",
		"distribution_arch":       "arch",
		"distribution_versions":   "array_to_string(versions, ',')",
		"package_count":           "package_count",
		"last_introspection_time": "last_introspection_time",
		"status":                  "status",
	}

	order := convertSortByToSQL(pageData.SortBy, sortMap)

	filteredDB.Order(order).Find(&repoConfigs).Count(&totalRepos)
	filteredDB.Preload("Repository").Limit(pageData.Limit).Offset(pageData.Offset).Find(&repoConfigs)

	if filteredDB.Error != nil {
		return api.RepositoryCollectionResponse{}, totalRepos, filteredDB.Error
	}
	repos := convertToResponses(repoConfigs)
	return api.RepositoryCollectionResponse{Data: repos}, totalRepos, nil
}

func (r repositoryConfigDaoImpl) InternalOnly_FetchRepoConfigsForRepoUUID(uuid string) []api.RepositoryResponse {
	repoConfigs := make([]models.RepositoryConfiguration, 0)
	filteredDB := r.db.Where("repositories.uuid = ?", uuid).
		Joins("inner join repositories on repository_configurations.repository_uuid = repositories.uuid")

	filteredDB.Preload("Repository").Find(&repoConfigs)

	if filteredDB.Error != nil {
		log.Error().Msgf("Unable to ListRepos: %v", uuid)
		return []api.RepositoryResponse{}
	}

	return convertToResponses(repoConfigs)
}

func (r repositoryConfigDaoImpl) Fetch(orgID string, uuid string) (api.RepositoryResponse, error) {
	repo := api.RepositoryResponse{}
	repoConfig, err := r.fetchRepoConfig(orgID, uuid)

	if err != nil {
		return repo, err
	}
	ModelToApiFields(repoConfig, &repo)
	return repo, err
}

func (r repositoryConfigDaoImpl) fetchRepoConfig(orgID string, uuid string) (models.RepositoryConfiguration, error) {
	found := models.RepositoryConfiguration{}
	result := r.db.
		Preload("Repository").
		Where("text(UUID) = ? AND ORG_ID = ?", uuid, orgID).
		First(&found)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return found, &ce.DaoError{NotFound: true, Message: "Could not find repository with UUID " + uuid}
		} else {
			return found, DBErrorToApi(result.Error)
		}
	}
	return found, nil
}

func (r repositoryConfigDaoImpl) FetchByRepoUuid(orgID string, repoUuid string) (api.RepositoryResponse, error) {
	repoConfig := models.RepositoryConfiguration{}
	repo := api.RepositoryResponse{}

	result := r.db.
		Preload("Repository").
		Joins("Inner join repositories on repositories.uuid = repository_configurations.repository_uuid").
		Where("text(Repositories.UUID) = ? AND ORG_ID = ?", repoUuid, orgID).
		First(&repoConfig)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return repo, &ce.DaoError{NotFound: true, Message: "Could not find repository with UUID " + repoUuid}
		} else {
			return repo, DBErrorToApi(result.Error)
		}
	}
	ModelToApiFields(repoConfig, &repo)
	return repo, nil
}

// Update updates a RepositoryConfig with changed parameters.  Returns whether the url changed, and an error if updating failed
func (r repositoryConfigDaoImpl) Update(orgID, uuid string, repoParams api.RepositoryRequest) (bool, error) {
	var repo models.Repository
	var repoConfig models.RepositoryConfiguration
	var err error
	updatedUrl := false

	// We are updating the repo config & snapshots, so bundle in a transaction
	err = r.db.Transaction(func(tx *gorm.DB) error {
		if repoConfig, err = r.fetchRepoConfig(orgID, uuid); err != nil {
			return err
		}
		ApiFieldsToModel(repoParams, &repoConfig, &repo)

		// If URL is included in params, search for existing
		// Repository record, or create a new one.
		// Then replace existing Repository/RepoConfig association.
		if repoParams.URL != nil {
			cleanedUrl := models.CleanupURL(*repoParams.URL)
			err = tx.FirstOrCreate(&repo, "url = ?", cleanedUrl).Error
			if err != nil {
				return DBErrorToApi(err)
			}
			oldRepoUuid := repoConfig.RepositoryUUID
			repoConfig.RepositoryUUID = repo.UUID
			updatedUrl = true
			err = r.updateSnapshotRepo(tx, oldRepoUuid, repo.UUID, orgID)
			if err != nil {
				return DBErrorToApi(err)
			}
		}

		repoConfig.Repository = models.Repository{}
		if err := tx.Model(&repoConfig).Updates(repoConfig.MapForUpdate()).Error; err != nil {
			return DBErrorToApi(err)
		}

		repositoryResponse := api.RepositoryResponse{}
		ModelToApiFields(repoConfig, &repositoryResponse)

		notifications.SendNotification(
			orgID,
			notifications.RepositoryUpdated,
			[]repositories.Repositories{notifications.MapRepositoryResponse(repositoryResponse)},
		)
		return nil
	})
	if err != nil {
		return updatedUrl, err
	}

	repositoryResponse := api.RepositoryResponse{}
	ModelToApiFields(repoConfig, &repositoryResponse)

	notifications.SendNotification(
		orgID,
		notifications.RepositoryUpdated,
		[]repositories.Repositories{notifications.MapRepositoryResponse(repositoryResponse)},
	)

	repoConfig.Repository = models.Repository{}
	if err := r.db.Model(&repoConfig).Updates(repoConfig.MapForUpdate()).Error; err != nil {
		return updatedUrl, DBErrorToApi(err)
	}

	return updatedUrl, nil
}

func (r repositoryConfigDaoImpl) updateSnapshotRepo(tx *gorm.DB, oldRepoUuid string, newRepoUuid string, orgId string) error {
	db := r.db
	if tx != nil {
		db = tx
	}
	var snap models.Snapshot
	result := db.Model(&snap).Where("org_id = ? and repository_uuid = ?", orgId, oldRepoUuid).Update("repository_uuid", newRepoUuid)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// SavePublicRepos saves a list of urls and marks them as "Public"
// This is meant for the list of repositories that are preloaded for all
// users.
func (r repositoryConfigDaoImpl) SavePublicRepos(urls []string) error {
	var repos []models.Repository

	for i := 0; i < len(urls); i++ {
		repos = append(repos, models.Repository{URL: urls[i], Public: true})
	}
	result := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "url"}},
		DoNothing: true}).Create(&repos)
	return result.Error
}

func (r repositoryConfigDaoImpl) Delete(orgID string, uuid string) error {
	var repoConfig models.RepositoryConfiguration
	var err error

	if repoConfig, err = r.fetchRepoConfig(orgID, uuid); err != nil {
		return err
	}

	if err = r.db.Delete(&repoConfig).Error; err != nil {
		return err
	}

	repositoryResponse := api.RepositoryResponse{}
	ModelToApiFields(repoConfig, &repositoryResponse)

	notifications.SendNotification(
		orgID,
		notifications.RepositoryDeleted,
		[]repositories.Repositories{notifications.MapRepositoryResponse(repositoryResponse)},
	)

	return nil
}

func (r repositoryConfigDaoImpl) BulkDelete(orgID string, uuids []string) []error {
	var responses []api.RepositoryResponse
	var errs []error

	_ = r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		responses, errs = r.bulkDelete(tx, orgID, uuids)
		if len(errs) > 0 {
			err = errors.New("rollback bulk delete")
		}
		return err
	})

	if len(responses) > 0 {
		mappedValues := make([]repositories.Repositories, len(responses))
		for i := 0; i < len(responses); i++ {
			mappedValues[i] = notifications.MapRepositoryResponse(responses[i])
		}
		notifications.SendNotification(orgID, notifications.RepositoryDeleted, mappedValues)
	}

	return errs
}

func (r repositoryConfigDaoImpl) bulkDelete(tx *gorm.DB, orgID string, uuids []string) ([]api.RepositoryResponse, []error) {
	var dbErr error
	size := len(uuids)
	errors := make([]error, size)
	responses := make([]api.RepositoryResponse, size)
	const save = "beforedelete"

	tx.SavePoint(save)
	for i := 0; i < size; i++ {
		var err error
		var repoConfig models.RepositoryConfiguration

		if repoConfig, err = r.fetchRepoConfig(orgID, uuids[i]); err != nil {
			dbErr = DBErrorToApi(err)
			errors[i] = dbErr
			tx.RollbackTo(save)
			continue
		}

		if err = tx.Delete(&repoConfig).Error; err != nil {
			dbErr = DBErrorToApi(err)
			errors[i] = dbErr
			tx.RollbackTo(save)
			continue
		}

		if dbErr == nil {
			ModelToApiFields(repoConfig, &responses[i])
		}
	}

	if dbErr == nil {
		return responses, []error{}
	} else {
		return []api.RepositoryResponse{}, errors
	}
}

func ApiFieldsToModel(apiRepo api.RepositoryRequest, repoConfig *models.RepositoryConfiguration, repo *models.Repository) {
	if apiRepo.Name != nil {
		repoConfig.Name = *apiRepo.Name
	}
	if apiRepo.DistributionArch != nil {
		repoConfig.Arch = *apiRepo.DistributionArch
	}
	if apiRepo.DistributionVersions != nil {
		repoConfig.Versions = *apiRepo.DistributionVersions
	}
	if apiRepo.URL != nil {
		repo.URL = *apiRepo.URL
	}
	if apiRepo.GpgKey != nil {
		repoConfig.GpgKey = *apiRepo.GpgKey
	}
	if apiRepo.MetadataVerification != nil {
		repoConfig.MetadataVerification = *apiRepo.MetadataVerification
	}
	if apiRepo.Snapshot != nil {
		repoConfig.Snapshot = *apiRepo.Snapshot
	}
}

func ModelToApiFields(repoConfig models.RepositoryConfiguration, apiRepo *api.RepositoryResponse) {
	apiRepo.UUID = repoConfig.UUID
	apiRepo.PackageCount = repoConfig.Repository.PackageCount
	apiRepo.URL = repoConfig.Repository.URL
	apiRepo.Name = repoConfig.Name
	apiRepo.DistributionVersions = repoConfig.Versions
	apiRepo.DistributionArch = repoConfig.Arch
	apiRepo.AccountID = repoConfig.AccountID
	apiRepo.OrgID = repoConfig.OrgID
	apiRepo.Status = repoConfig.Repository.Status
	apiRepo.GpgKey = repoConfig.GpgKey
	apiRepo.MetadataVerification = repoConfig.MetadataVerification
	apiRepo.FailedIntrospectionsCount = repoConfig.Repository.FailedIntrospectionsCount
	apiRepo.RepositoryUUID = repoConfig.RepositoryUUID
	apiRepo.Snapshot = repoConfig.Snapshot

	if repoConfig.Repository.LastIntrospectionTime != nil {
		apiRepo.LastIntrospectionTime = repoConfig.Repository.LastIntrospectionTime.Format(time.RFC3339)
	}
	if repoConfig.Repository.LastIntrospectionSuccessTime != nil {
		apiRepo.LastIntrospectionSuccessTime = repoConfig.Repository.LastIntrospectionSuccessTime.Format(time.RFC3339)
	}
	if repoConfig.Repository.LastIntrospectionUpdateTime != nil {
		apiRepo.LastIntrospectionUpdateTime = repoConfig.Repository.LastIntrospectionUpdateTime.Format(time.RFC3339)
	}
	if repoConfig.Repository.LastIntrospectionError != nil {
		apiRepo.LastIntrospectionError = *repoConfig.Repository.LastIntrospectionError
	}
}

// Converts the database models to our response objects
func convertToResponses(repoConfigs []models.RepositoryConfiguration) []api.RepositoryResponse {
	repos := make([]api.RepositoryResponse, len(repoConfigs))
	for i := 0; i < len(repoConfigs); i++ {
		ModelToApiFields(repoConfigs[i], &repos[i])
	}
	return repos
}

func isTimeout(err error) bool {
	timeout, ok := err.(interface {
		Timeout() bool
	})
	if ok && timeout.Timeout() {
		return true
	}
	return false
}

func (r repositoryConfigDaoImpl) ValidateParameters(orgId string, params api.RepositoryValidationRequest, excludedUUIDS []string) (api.RepositoryValidationResponse, error) {
	var (
		err      error
		response api.RepositoryValidationResponse
	)

	response.Name = api.GenericAttributeValidationResponse{}
	if params.Name == nil {
		response.Name.Skipped = true
	} else {
		err = r.validateName(orgId, *params.Name, &response.Name, excludedUUIDS)
		if err != nil {
			return response, err
		}
	}

	response.URL = api.UrlValidationResponse{}
	if params.URL == nil {
		response.URL.Skipped = true
	} else {
		url := models.CleanupURL(*params.URL)
		err = r.validateUrl(orgId, url, &response, excludedUUIDS)
		if err != nil {
			return response, err
		}
		if response.URL.Valid {
			r.yumRepo.Configure(yum.YummySettings{URL: &url, Client: http.DefaultClient})
			r.validateMetadataPresence(&response)
			if response.URL.MetadataPresent {
				r.checkSignaturePresent(&params, &response)
			}
		}
	}
	return response, err
}

func (r repositoryConfigDaoImpl) validateName(orgId string, name string, response *api.GenericAttributeValidationResponse, excludedUUIDS []string) error {
	if name == "" {
		response.Valid = false
		response.Error = "Name cannot be blank"
		return nil
	}

	found := models.RepositoryConfiguration{}
	query := r.db.Where("name = ? AND ORG_ID = ?", name, orgId)
	if len(excludedUUIDS) != 0 {
		query = query.Where("repository_configurations.uuid NOT IN ?", excludedUUIDS)
	}
	if err := query.Find(&found).Error; err != nil {
		response.Valid = false
		return err
	}

	if found.UUID != "" {
		response.Valid = false
		response.Error = fmt.Sprintf("A repository with the name '%s' already exists.", name)
		return nil
	}

	response.Valid = true
	return nil
}

func (r repositoryConfigDaoImpl) validateUrl(orgId string, url string, response *api.RepositoryValidationResponse, excludedUUIDS []string) error {
	if url == "" {
		response.URL.Valid = false
		response.URL.Error = "URL cannot be blank"
		return nil
	}

	found := models.RepositoryConfiguration{}
	query := r.db.Preload("Repository").
		Joins("inner join repositories on repository_configurations.repository_uuid = repositories.uuid").
		Where("Repositories.URL = ? AND ORG_ID = ?", url, orgId)
	if len(excludedUUIDS) != 0 {
		query = query.Where("repository_configurations.uuid NOT IN ?", excludedUUIDS)
	}
	if err := query.Find(&found).Error; err != nil {
		response.URL.Valid = false
		return err
	}

	if found.UUID != "" {
		response.URL.Valid = false
		response.URL.Error = fmt.Sprintf("A repository with the URL '%s' already exists.", url)
		return nil
	}

	containsWhitespace := strings.ContainsAny(strings.TrimSpace(url), " \t\n\v\r\f")
	if containsWhitespace {
		response.URL.Valid = false
		response.URL.Error = "URL cannot contain whitespace."
		return nil
	}

	response.URL.Valid = true
	return nil
}

func (r repositoryConfigDaoImpl) validateMetadataPresence(response *api.RepositoryValidationResponse) {
	_, code, err := r.yumRepo.Repomd()
	if err != nil {
		response.URL.HTTPCode = code
		if isTimeout(err) {
			response.URL.Error = fmt.Sprintf("Error fetching YUM metadata: %s", "Timeout occurred")
		} else {
			response.URL.Error = fmt.Sprintf("Error fetching YUM metadata: %s", err.Error())
		}
		response.URL.MetadataPresent = false
	} else {
		response.URL.HTTPCode = code
		response.URL.MetadataPresent = code >= 200 && code < 300
	}
}

func (r repositoryConfigDaoImpl) checkSignaturePresent(request *api.RepositoryValidationRequest, response *api.RepositoryValidationResponse) {
	if request.GPGKey == nil || *request.GPGKey == "" {
		response.GPGKey.Skipped = true
		response.GPGKey.Valid = true
	} else {
		_, err := LoadGpgKey(request.GPGKey)
		if err == nil {
			response.GPGKey.Valid = true
		} else {
			response.GPGKey.Valid = false
			response.GPGKey.Error = fmt.Sprintf("Error loading GPG Key: %s.  Is this a valid GPG Key?", err.Error())
		}
	}

	sig, _, err := r.yumRepo.Signature()
	if err != nil || sig == nil {
		response.URL.MetadataSignaturePresent = false
	} else {
		response.URL.MetadataSignaturePresent = true
		if response.GPGKey.Valid && !response.GPGKey.Skipped && request.MetadataVerification { // GPG key is valid & signature present, so validate the signature
			sigErr := ValidateSignature(r.yumRepo, request.GPGKey)
			if sigErr == nil {
				response.GPGKey.Valid = true
			} else if response.GPGKey.Error == "" {
				response.GPGKey.Valid = false
				response.GPGKey.Error = fmt.Sprintf("Error validating signature: %s. Is this the correct GPG Key?", sigErr.Error())
			}
		}
	}
}

func LoadGpgKey(gpgKey *string) (openpgp.EntityList, error) {
	var keyRing, entity openpgp.EntityList
	var err error

	gpgKeys, err := readGpgKeys(gpgKey)
	if err != nil {
		return nil, err
	}
	for _, k := range gpgKeys {
		entity, err = openpgp.ReadArmoredKeyRing(strings.NewReader(k))
		if err != nil {
			return nil, err
		}
		keyRing = append(keyRing, entity[0])
	}
	return keyRing, nil
}

// readGpgKeys openpgp.ReadArmoredKeyRing does not correctly parse multiple gpg keys from one file.
// This is a work around that returns a list of gpgKey strings to be passed individually
// to openpgp.ReadArmoredKeyRing
func readGpgKeys(gpgKey *string) ([]string, error) {
	if gpgKey == nil {
		return nil, fmt.Errorf("gpg key cannot be nil")
	}
	const EndGpgKey = "-----END PGP PUBLIC KEY BLOCK-----"
	var gpgKeys []string
	gpgKeyCopy := *gpgKey

	for {
		val := strings.Index(gpgKeyCopy, EndGpgKey)
		if val == -1 {
			break
		}
		gpgKeys = append(gpgKeys, gpgKeyCopy[:val+len(EndGpgKey)])
		gpgKeyCopy = gpgKeyCopy[val+len(EndGpgKey):]
	}

	if len(gpgKeys) == 0 {
		return nil, fmt.Errorf("no gpg key was found")
	}
	return gpgKeys, nil
}

func ValidateSignature(repo yum.YumRepository, gpgKey *string) error {
	keyRing, err := LoadGpgKey(gpgKey)
	if err != nil {
		return err
	}

	repomd, _, _ := repo.Repomd()
	signedFileString := repomd.RepomdString
	sig, _, _ := repo.Signature()
	_, err = openpgp.CheckArmoredDetachedSignature(keyRing, strings.NewReader(*signedFileString), strings.NewReader(*sig), nil)
	if err != nil {
		return err
	}
	return nil
}
