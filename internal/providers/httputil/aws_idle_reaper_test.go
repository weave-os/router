package httputil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const awsFixedIdleReaper = 350 * time.Second

func TestByteIdleWatchdogsStayUnderAWSIdleReaper(t *testing.T) {
	assert.Less(t, DefaultSSEIdleTimeout, awsFixedIdleReaper)
	assert.Less(t, DefaultResponsesSSEIdleTimeout, awsFixedIdleReaper)
}
