package accountroles

import (
	"fmt"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift/rosa/pkg/aws"
	awscb "github.com/openshift/rosa/pkg/aws/commandbuilder"
	"github.com/openshift/rosa/pkg/aws/tags"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/rosa"
)

type creator interface {
	createRoles(*rosa.Runtime, *accountRolesCreationInput) error
	getRoleTags(string, *accountRolesCreationInput) map[string]string
	buildCommands(*accountRolesCreationInput) (string, error)
}

func initCreator(managedPolicies bool) creator {
	if managedPolicies {
		return &managedPoliciesCreator{}
	}

	return &unmanagedPoliciesCreator{}
}

type accountRolesCreationInput struct {
	prefix               string
	permissionsBoundary  string
	accountID            string
	env                  string
	policies             map[string]*cmv1.AWSSTSPolicy
	defaultPolicyVersion string
	path                 string
}

func buildRolesCreationInput(prefix, permissionsBoundary, accountID, env string,
	policies map[string]*cmv1.AWSSTSPolicy, defaultPolicyVersion string,
	path string) *accountRolesCreationInput {
	return &accountRolesCreationInput{
		prefix:               prefix,
		permissionsBoundary:  permissionsBoundary,
		accountID:            accountID,
		env:                  env,
		policies:             policies,
		defaultPolicyVersion: defaultPolicyVersion,
		path:                 path,
	}
}

type managedPoliciesCreator struct{}

func (mp *managedPoliciesCreator) createRoles(r *rosa.Runtime, input *accountRolesCreationInput) error {
	for file, role := range aws.AccountRoles {
		accRoleName := aws.GetRoleName(input.prefix, role.Name)
		if !confirm.Prompt(true, "Create the '%s' role?", accRoleName) {
			continue
		}

		assumeRolePolicy := getAssumeRolePolicy(file, input)

		r.Reporter.Debugf("Creating role '%s'", accRoleName)
		tagsList := mp.getRoleTags(file, input)
		roleARN, err := r.AWSClient.EnsureRole(accRoleName, assumeRolePolicy, input.permissionsBoundary,
			input.defaultPolicyVersion, tagsList, input.path, true)
		if err != nil {
			return err
		}
		r.Reporter.Infof("Created role '%s' with ARN '%s'", accRoleName, roleARN)

		filename := fmt.Sprintf("sts_%s_permission_policy", file)
		var policyARN string
		policyARN, err = aws.GetManagedPolicyARN(input.policies, filename)
		if err != nil {
			return err
		}

		r.Reporter.Debugf("Attaching permission policy to role '%s'", filename)
		err = r.AWSClient.AttachRolePolicy(accRoleName, policyARN)
		if err != nil {
			return err
		}
	}

	return nil
}

func (mp *managedPoliciesCreator) buildCommands(input *accountRolesCreationInput) (string, error) {
	commands := []string{}
	for file, role := range aws.AccountRoles {
		accRoleName := aws.GetRoleName(input.prefix, role.Name)
		iamTags := mp.getRoleTags(file, input)

		createRole := buildCreateRoleCommand(accRoleName, file, iamTags, input)

		policyARN, err := aws.GetManagedPolicyARN(input.policies, fmt.Sprintf("sts_%s_permission_policy", file))
		if err != nil {
			return "", err
		}

		attachRolePolicy := buildAttachRolePolicyCommand(accRoleName, policyARN)

		commands = append(commands, createRole, attachRolePolicy)
	}

	return awscb.JoinCommands(commands), nil
}

func (mp *managedPoliciesCreator) getRoleTags(roleType string, input *accountRolesCreationInput) map[string]string {
	tagsList := getBaseRoleTags(roleType, input)
	tagsList[tags.ManagedPolicies] = "true"

	return tagsList
}

type unmanagedPoliciesCreator struct{}

func (up *unmanagedPoliciesCreator) createRoles(r *rosa.Runtime, input *accountRolesCreationInput) error {
	for file, role := range aws.AccountRoles {
		accRoleName := aws.GetRoleName(input.prefix, role.Name)
		if !confirm.Prompt(true, "Create the '%s' role?", accRoleName) {
			continue
		}

		assumeRolePolicy := getAssumeRolePolicy(file, input)

		r.Reporter.Debugf("Creating role '%s'", accRoleName)
		tagsList := up.getRoleTags(file, input)
		roleARN, err := r.AWSClient.EnsureRole(accRoleName, assumeRolePolicy, input.permissionsBoundary,
			input.defaultPolicyVersion, tagsList, input.path, false)
		if err != nil {
			return err
		}
		r.Reporter.Infof("Created role '%s' with ARN '%s'", accRoleName, roleARN)

		filename := fmt.Sprintf("sts_%s_permission_policy", file)
		policyPermissionDetail := aws.GetPolicyDetails(input.policies, filename)

		policyARN := aws.GetPolicyARN(r.Creator.AccountID, accRoleName, input.path)

		r.Reporter.Debugf("Creating permission policy '%s'", policyARN)
		if args.forcePolicyCreation {
			policyARN, err = r.AWSClient.ForceEnsurePolicy(policyARN, policyPermissionDetail,
				input.defaultPolicyVersion, tagsList, input.path)
		} else {
			policyARN, err = r.AWSClient.EnsurePolicy(policyARN, policyPermissionDetail,
				input.defaultPolicyVersion, tagsList, input.path)
		}
		if err != nil {
			return err
		}

		r.Reporter.Debugf("Attaching permission policy to role '%s'", filename)
		err = r.AWSClient.AttachRolePolicy(accRoleName, policyARN)
		if err != nil {
			return err
		}
	}

	return nil
}

func (up *unmanagedPoliciesCreator) buildCommands(input *accountRolesCreationInput) (string, error) {
	commands := []string{}
	for file, role := range aws.AccountRoles {
		accRoleName := aws.GetRoleName(input.prefix, role.Name)
		iamTags := up.getRoleTags(file, input)

		createRole := buildCreateRoleCommand(accRoleName, file, iamTags, input)

		policyName := aws.GetPolicyName(accRoleName)
		createPolicy := awscb.NewIAMCommandBuilder().
			SetCommand(awscb.CreatePolicy).
			AddParam(awscb.PolicyName, policyName).
			AddParam(awscb.PolicyDocument, fmt.Sprintf("file://sts_%s_permission_policy.json", file)).
			AddTags(iamTags).
			AddParam(awscb.Path, input.path).
			Build()

		policyARN := aws.GetPolicyARN(input.accountID, accRoleName, input.path)

		attachRolePolicy := buildAttachRolePolicyCommand(accRoleName, policyARN)

		commands = append(commands, createRole, createPolicy, attachRolePolicy)
	}

	return awscb.JoinCommands(commands), nil
}

func (up *unmanagedPoliciesCreator) getRoleTags(roleType string, input *accountRolesCreationInput) map[string]string {
	return getBaseRoleTags(roleType, input)
}

func getAssumeRolePolicy(file string, input *accountRolesCreationInput) string {
	filename := fmt.Sprintf("sts_%s_trust_policy", file)
	policyDetail := aws.GetPolicyDetails(input.policies, filename)

	return aws.InterpolatePolicyDocument(policyDetail, map[string]string{
		"partition":      aws.GetPartition(),
		"aws_account_id": aws.GetJumpAccount(input.env),
	})
}

func getBaseRoleTags(roleType string, input *accountRolesCreationInput) map[string]string {
	return map[string]string{
		tags.OpenShiftVersion: input.defaultPolicyVersion,
		tags.RolePrefix:       input.prefix,
		tags.RoleType:         roleType,
		tags.RedHatManaged:    "true",
	}
}

func buildCreateRoleCommand(accRoleName string, file string, iamTags map[string]string,
	input *accountRolesCreationInput) string {
	return awscb.NewIAMCommandBuilder().
		SetCommand(awscb.CreateRole).
		AddParam(awscb.RoleName, accRoleName).
		AddParam(awscb.AssumeRolePolicyDocument, fmt.Sprintf("file://sts_%s_trust_policy.json", file)).
		AddParam(awscb.PermissionsBoundary, input.permissionsBoundary).
		AddTags(iamTags).
		AddParam(awscb.Path, input.path).
		Build()
}

func buildAttachRolePolicyCommand(accRoleName string, policyARN string) string {
	return awscb.NewIAMCommandBuilder().
		SetCommand(awscb.AttachRolePolicy).
		AddParam(awscb.RoleName, accRoleName).
		AddParam(awscb.PolicyArn, policyARN).
		Build()
}