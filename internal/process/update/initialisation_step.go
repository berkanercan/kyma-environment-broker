package update

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/kyma-project/kyma-environment-broker/internal/storage/dberr"

	"github.com/kyma-project/kyma-environment-broker/internal"
	kebError "github.com/kyma-project/kyma-environment-broker/internal/error"
	"github.com/kyma-project/kyma-environment-broker/internal/process"
	"github.com/kyma-project/kyma-environment-broker/internal/storage"
	"github.com/pivotal-cf/brokerapi/v12/domain"
)

type InitialisationStep struct {
	operationManager *process.OperationManager
	operationStorage storage.Operations
	instanceStorage  storage.Instances
}

func NewInitialisationStep(db storage.BrokerStorage) *InitialisationStep {
	step := &InitialisationStep{
		operationStorage: db.Operations(),
		instanceStorage:  db.Instances(),
	}
	step.operationManager = process.NewOperationManager(step.operationStorage, step.Name(), kebError.KEBDependency)
	return step
}

func (s *InitialisationStep) Name() string {
	return "Update_Kyma_Initialisation"
}

func (s *InitialisationStep) Run(operation internal.Operation, log *slog.Logger) (internal.Operation, time.Duration, error) {
	// Check concurrent deprovisioning (or suspension) operation (launched after target resolution)
	// Terminate (preempt) upgrade immediately with succeeded
	lastOp, err := s.operationStorage.GetLastOperation(operation.InstanceID)
	if err != nil {
		return operation, time.Minute, nil
	}

	if operation.State == internal.OperationStatePending {
		if !lastOp.IsFinished() {
			log.Info(fmt.Sprintf("waiting for %s operation (%s) to be finished", lastOp.Type, lastOp.ID))
			return operation, time.Minute, nil
		}

		// read the instance details (it could happen that created updating operation has outdated one)
		instance, err := s.instanceStorage.GetByID(operation.InstanceID)
		if err != nil {
			if dberr.IsNotFound(err) {
				log.Warn("the instance already deprovisioned")
				return s.operationManager.OperationFailed(operation, "the instance was already deprovisioned", err, log)
			}
			return operation, time.Second, nil
		}
		instance.Parameters.ErsContext = internal.InheritMissingERSContext(instance.Parameters.ErsContext, operation.ProvisioningParameters.ErsContext)
		if _, err := s.instanceStorage.Update(*instance); err != nil {
			log.Error("unable to update the instance, retrying")
			return operation, time.Second, err
		}

		// suspension cleared runtimeID
		if operation.RuntimeID == "" {
			err = s.getRuntimeIdFromProvisioningOp(&operation)
			if err != nil {
				return s.operationManager.RetryOperation(operation, "error while getting runtime ID", err, 5*time.Second, 1*time.Minute, log)
			}
		}
		log.Info(fmt.Sprintf("Got runtime ID %s", operation.RuntimeID))

		op, delay, _ := s.operationManager.UpdateOperation(operation, func(op *internal.Operation) {
			op.State = domain.InProgress
			op.InstanceDetails = instance.InstanceDetails
			if op.ProvisioningParameters.ErsContext.SMOperatorCredentials == nil && lastOp.ProvisioningParameters.ErsContext.SMOperatorCredentials != nil {
				op.ProvisioningParameters.ErsContext.SMOperatorCredentials = lastOp.ProvisioningParameters.ErsContext.SMOperatorCredentials
			}
			op.ProvisioningParameters.ErsContext = internal.InheritMissingERSContext(op.ProvisioningParameters.ErsContext, lastOp.ProvisioningParameters.ErsContext)
		}, log)
		if delay != 0 {
			log.Error("unable to update the operation (move to 'in progress'), retrying")
			return operation, delay, nil
		}

		operation = op
	}

	log.Info("updating last operation id in the instance")
	err = s.instanceStorage.UpdateInstanceLastOperation(operation.InstanceID, operation.ID)
	if err != nil {
		return s.operationManager.RetryOperation(operation, "error while updating last operation ID", err, 5*time.Second, 1*time.Minute, log)
	}

	if lastOp.Type == internal.OperationTypeDeprovision {
		return s.operationManager.OperationSucceeded(operation, fmt.Sprintf("operation preempted by deprovisioning %s", lastOp.ID), log)
	}

	return operation, 0, nil
}

func (s *InitialisationStep) getRuntimeIdFromProvisioningOp(operation *internal.Operation) error {
	provOp, err := s.operationStorage.GetProvisioningOperationByInstanceID(operation.InstanceID)
	if err != nil {
		return fmt.Errorf("cannot get last provisioning operation for runtime id")
	}
	operation.RuntimeID = provOp.RuntimeID
	return nil
}
