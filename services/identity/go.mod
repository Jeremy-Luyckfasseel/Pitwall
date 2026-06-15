module github.com/Jeremy-Luyckfasseel/Pitwall/services/identity

go 1.26.4

replace github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall => ../../libs/go-pitwall

require (
	github.com/Jeremy-Luyckfasseel/Pitwall/libs/go-pitwall v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
)

require (
	github.com/rabbitmq/amqp091-go v1.11.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.36.0 // indirect
)
