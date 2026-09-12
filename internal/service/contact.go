package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type ContactInput struct {
	Name     string
	IDNumber string
	Phone    string
	Relation string
}

func (s *Service) CreateContact(ctx context.Context, in ContactInput) (*Contact, error) {
	if in.Name == "" || in.IDNumber == "" || in.Phone == "" {
		return nil, ErrBadRequest
	}
	var c Contact
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO contacts (name, id_number, phone, relation)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id_number) DO UPDATE
		  SET name = EXCLUDED.name, phone = EXCLUDED.phone, relation = EXCLUDED.relation
		RETURNING id, name, id_number, phone, relation, created_at`,
		in.Name, in.IDNumber, in.Phone, in.Relation).
		Scan(&c.ID, &c.Name, &c.IDNumber, &c.Phone, &c.Relation, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Service) GetContact(ctx context.Context, id int64) (*Contact, error) {
	var c Contact
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, id_number, phone, relation, created_at FROM contacts WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.IDNumber, &c.Phone, &c.Relation, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}
