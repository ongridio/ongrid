#!/usr/bin/env ruby
# Validate the chooser and common Issue Form fields without installing dependencies.
# ponytail: extend these checks when templates adopt more field-specific options.
require 'yaml'
require 'uri'

def text!(value)
  raise 'expected non-empty text' unless value.is_a?(String) && !value.strip.empty?
end

def boolean!(value)
  raise 'expected a boolean' unless [true, false].include?(value)
end

files = Dir['.github/ISSUE_TEMPLATE/*.{yml,yaml}'].sort
files.each do |file|
  begin
    data = YAML.safe_load(File.read(file))
    raise 'expected a YAML mapping' unless data.is_a?(Hash)
    if File.basename(file).match?(/\Aconfig\.ya?ml\z/)
      boolean!(data['blank_issues_enabled']) if data.key?('blank_issues_enabled')
      links = data.fetch('contact_links', [])
      raise 'contact_links must be a list' unless links.is_a?(Array)
      links.each do |link|
        %w[name url about].each { |key| text!(link.fetch(key)) }
        uri = URI.parse(link['url'])
        raise 'contact URL must use HTTP(S)' unless %w[http https].include?(uri.scheme) && uri.host
      end
      next
    end

    %w[name description].each { |key| text!(data.fetch(key)) }
    body = data.fetch('body')
    raise 'body must contain form fields' unless body.is_a?(Array) && body.any? { |field| field['type'] != 'markdown' }
    ids = []
    body.each do |field|
      type = field.fetch('type')
      raise 'unknown form field type' unless %w[markdown input textarea dropdown checkboxes upload].include?(type)
      if field.key?('id')
        id = field['id']
        raise 'invalid or duplicate field id' unless id.is_a?(String) && id.match?(/\A[a-zA-Z0-9_-]+\z/) && !ids.include?(id)
        ids << id
      end
      attributes = field.fetch('attributes')
      text!(attributes.fetch(type == 'markdown' ? 'value' : 'label'))
      validations = field.fetch('validations', {})
      boolean!(validations['required']) if validations.key?('required')
      boolean!(attributes['multiple']) if attributes.key?('multiple')
      next unless %w[dropdown checkboxes].include?(type)

      options = attributes.fetch('options')
      raise 'options must be a non-empty list' unless options.is_a?(Array) && !options.empty?
      labels = options.map do |option|
        if type == 'checkboxes'
          boolean!(option['required']) if option.key?('required')
          option.fetch('label')
        else
          option
        end
      end
      labels.each { |label| text!(label) }
      raise 'duplicate options' unless labels.uniq == labels
    end
  rescue StandardError => error
    abort "#{file}: #{error.message}"
  end
end
puts "Issue template checks passed (#{files.length} YAML files)"
