package billing

import "context"

type AdmissionScope struct {
	OrganizationID, KeyID       string
	IssuerID, SessionID, TaskID string
	Delegated                   bool
}

type Admission struct {
	Balance                 CheckResult
	BalanceFound            bool
	Key                     APIKeySpendCapResult
	Monthly                 MonthlySpendResult
	SessionSpent, TaskSpent int64
	DelegatedErr            error
}

type AdmissionReader interface {
	ReadAdmission(context.Context, AdmissionScope) (Admission, error)
}

func (s *Service) AdmissionReader() AdmissionReader {
	return s.admission
}

func (s *Service) WithAdmissionReader(reader interface {
	AdmissionReader
	DelegatedSpendReader
}) *Service {
	s.admission = reader
	return s.WithDelegatedSpend(reader)
}
