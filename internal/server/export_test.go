package server

// Exports for the server_test package — keeps the internal helpers
// unexported in the production API while letting black-box tests
// exercise them by their canonical name.
var PatDaysRemainingForTest = patDaysRemaining
