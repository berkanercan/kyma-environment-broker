package deprovisioning

import (
	"fmt"
	"time"

	"github.com/kyma-project/kyma-environment-broker/internal"
	kebErrors "github.com/kyma-project/kyma-environment-broker/internal/error"
	"github.com/kyma-project/kyma-environment-broker/internal/process"
	"github.com/kyma-project/kyma-environment-broker/internal/reconciler"
	"github.com/kyma-project/kyma-environment-broker/internal/storage"
	"github.com/sirupsen/logrus"
)

type DeregisterClusterStep struct {
	operationManager   *process.OperationManager
	reconcilerClient   reconciler.Client
	provisionerTimeout time.Duration
}

func NewDeregisterClusterStep(os storage.Operations, cli reconciler.Client) *DeregisterClusterStep {
	return &DeregisterClusterStep{
		operationManager: process.NewOperationManager(os),
		reconcilerClient: cli,
	}
}

func (s *DeregisterClusterStep) Name() string {
	return "Deregister_Cluster"
}

func (s *DeregisterClusterStep) Run(operation internal.Operation, log logrus.FieldLogger) (internal.Operation, time.Duration, error) {
	if operation.ClusterConfigurationVersion == 0 {
		log.Info("Cluster configuration was not created, skipping")
		return operation, 0, nil
	}
	if operation.ClusterConfigurationDeleted {
		log.Info("Cluster configuration was deleted, skipping")
		return operation, 0, nil
	}
	err := s.reconcilerClient.DeleteCluster(operation.RuntimeID)
	if err != nil {
		return s.handleError(operation, err, log, "cannot remove DataTenant")
	}

	modifiedOp, d, _ := s.operationManager.UpdateOperation(operation, func(op *internal.Operation) {
		op.ClusterConfigurationDeleted = true
		op.ReconcilerDeregistrationAt = time.Now()
	}, log)

	return modifiedOp, d, nil
}

func (s *DeregisterClusterStep) handleError(operation internal.Operation, err error, log logrus.FieldLogger, msg string) (internal.Operation, time.Duration, error) {
	log.Errorf("%s: %s", msg, err)

	if kebErrors.IsTemporaryError(err) {
		since := time.Since(operation.UpdatedAt)
		if since < 30*time.Minute {
			log.Errorf("request to the Reconciler failed: %s. Retry...", err)
			return operation, 15 * time.Second, nil
		}
	}
	errMsg := fmt.Sprintf("Reconciler cluster configuration have not been deleted in step %s.", s.Name())
	operation, repeat, err := s.operationManager.MarkStepAsExcutedButNotCompleted(operation, s.Name(), errMsg, log)
	if repeat != 0 {
		return operation, repeat, err
	}
	return operation, 0, nil
}
