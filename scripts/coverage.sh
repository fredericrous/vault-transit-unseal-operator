#!/bin/bash
# Script to calculate test coverage excluding infrastructure code

echo "Running tests with coverage for business logic packages..."
echo

# Run tests for business logic packages only (continue on failure)
go test \
  ./pkg/reconciler \
  ./pkg/vault \
  ./pkg/transit \
  ./pkg/errors \
  ./pkg/health \
  ./pkg/metrics \
  -coverprofile=business-cover.out || true

# Display coverage for business logic only
echo -e "\n=== Business Logic Coverage ==="
go tool cover -func=business-cover.out | grep -E "(reconciler|vault|transit|errors|health|metrics)" | grep -v test_helpers

# Show total coverage for business logic
echo -e "\n=== Total Business Logic Coverage ==="
go tool cover -func=business-cover.out | grep "total:"

# Optional: Generate HTML report
if [ "$1" = "--html" ]; then
  go tool cover -html=business-cover.out -o coverage-business.html
  echo -e "\nHTML report generated: coverage-business.html"
fi