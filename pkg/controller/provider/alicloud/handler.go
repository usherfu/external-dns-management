/*
 * Copyright 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 *
 */

package alicloud

import (
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
	"github.com/gardener/controller-manager-library/pkg/logger"
	"github.com/gardener/external-dns-management/pkg/dns"
	"github.com/gardener/external-dns-management/pkg/dns/provider"
)

type Handler struct {
	config provider.DNSHandlerConfig
	access Access
}

var _ provider.DNSHandler = &Handler{}

func NewHandler(logger logger.LogContext, config *provider.DNSHandlerConfig, metrics provider.Metrics) (provider.DNSHandler, error) {
	var err error

	this := &Handler{
		config: *config,
	}

	accessKeyId := this.config.Properties["ACCESS_KEY_ID"]
	if accessKeyId == "" {
		return nil, fmt.Errorf("'ACCESS_KEY_ID' required in secret")
	}
	accessKeySecret := this.config.Properties["ACCESS_KEY_SECRET"]
	if accessKeySecret == "" {
		return nil, fmt.Errorf("'ACCESS_KEY_SECRET' required in secret")
	}

	access, err := NewAccess(accessKeyId, accessKeySecret, metrics)
	if err != nil {
		return nil, err
	}

	this.access = access
	return this, nil
}

func (h *Handler) ProviderType() string {
	return TYPE_CODE
}

func (this *Handler) GetZones() (provider.DNSHostedZones, error) {
	raw := []alidns.Domain{}
	{
		f := func(zone alidns.Domain) (bool, error) {
			raw = append(raw, zone)
			return true, nil
		}
		err := this.access.ListDomains(f)
		if err != nil {
			return nil, err
		}
	}

	zones := provider.DNSHostedZones{}
	{
		for _, z := range raw {
			forwarded := []string{}
			f := func(r alidns.Record) (bool, error) {
				if r.Type == dns.RS_NS {
					name := GetDNSName(r)
					if name != z.DomainName {
						forwarded = append(forwarded, name)
					}
				}
				return true, nil
			}
			err := this.access.ListRecords(z.DomainName, f)
			if err != nil {
				return nil, err
			}
			hostedZone := provider.NewDNSHostedZone(
				this.ProviderType(), z.DomainId,
				z.DomainName, z.DomainName, forwarded)
			zones = append(zones, hostedZone)
		}
	}

	return zones, nil
}

func (this *Handler) GetZoneState(zone provider.DNSHostedZone) (provider.DNSZoneState, error) {

	state := newState()

	f := func(r alidns.Record) (bool, error) {
		state.addRecord(r)
		return true, nil
	}
	err := this.access.ListRecords(zone.Key(), f)
	if err != nil {
		return nil, err
	}
	state.calculateDNSSets()
	return state, nil
}

func (this *Handler) ExecuteRequests(logger logger.LogContext, zone provider.DNSHostedZone, state provider.DNSZoneState, reqs []*provider.ChangeRequest) error {

	exec := NewExecution(logger, this, state.(*zonestate), zone)
	for _, r := range reqs {
		exec.addChange(r)
	}
	if this.config.DryRun {
		logger.Infof("no changes in dryrun mode for AliCloud")
		return nil
	}
	return exec.submitChanges()
}
